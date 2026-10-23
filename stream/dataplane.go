// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"log"
	"sync"

	"github.com/barahn/remotekit/bark"
	"github.com/barahn/remotekit/clipboard"
	"github.com/barahn/remotekit/webrtc"
)

// dataPlane applies the messages that carry the user's data -- input,
// clipboard and file transfer -- whichever way they arrive: over a viewer's
// WebRTC data channel, or over the signalling socket, which is how viewers
// that predate the data channel send them.
//
// Both paths go through handle, so consent is checked in one place and the two
// cannot drift apart.
type dataPlane struct {
	r           *Runner
	clipMgr     clipboard.Manager
	clipWatcher *clipboard.Watcher
	// files receives the files viewers send; see fileinbox.go.
	files fileInbox

	// displays and selectDisplay reach the session's capturer; see
	// displays.go. Nil outside a session.
	displays      func() (bark.DisplaysPayload, bool)
	selectDisplay func(index int) error
}

// sender is who a data plane message came from: the viewer_id it is routed
// under, and, for a data channel under RequireSignedViewers, the key whose
// signed offer set the channel up.
type sender struct {
	id  string
	key ed25519.PublicKey
}

// replyFunc sends a response of the given type back the way the message it
// answers came.
type replyFunc func(msgType string, fields map[string]interface{})

// isDataPlane reports whether a message type carries user data.
func isDataPlane(msgType string) bool {
	switch msgType {
	case "input", "clipboard", "file_start", "file_chunk", "file_complete":
		return true
	}
	return false
}

// handle applies one data plane message. fields is its content: for input,
// the input event itself; for the rest, the message's own fields.
func (d *dataPlane) handle(ctx context.Context, from sender, msgType string, fields map[string]interface{}, reply replyFunc) {
	r := d.r
	switch msgType {
	case "input":
		r.handleInputPayload(fields)

	case "clipboard":
		// An empty text is a valid clipboard: it clears it.
		text, ok := fields["text"].(string)
		if ok && r.allow(PermissionClipboard, "clipboard write") {
			d.clipWatcher.UpdateLastText(text)
			_ = d.clipMgr.SetText(ctx, text)
			log.Printf("[AgentStream] Received and applied clipboard sync (%d bytes)\n", len(text))
			r.clipboardEvent(ClipboardEvent{Direction: ClipboardToHost, ViewerID: from.id, Key: from.key, Size: len(text)})
		}

	case "file_start", "file_chunk", "file_complete":
		d.files.omitPath = r.omitFilePathInReply
		d.files.handle(from, msgType, fields, "agent", func(stage string) string {
			if !r.allow(PermissionFileTransfer, stage) {
				return PermissionFileTransfer + " has not been granted"
			}
			return ""
		}, r.onFileOffer, true, r.onFileSaved, reply)
	}
}

// handleChannelMessage applies one message from a viewer's data channel. The
// message is a bark.Envelope; its reply goes back on the same channel, as an
// envelope too. Anything that is not a data plane message is ignored: the
// channel carries the user's data, not signalling.
func (d *dataPlane) handleChannelMessage(ctx context.Context, from sender, peer *webrtc.PeerSession, label string, msg []byte) {
	var env bark.Envelope
	if err := json.Unmarshal(msg, &env); err != nil {
		return
	}
	msgType := string(env.Type)
	if label == webrtc.DataChannelControl && isDisplayMessage(env.Type) {
		d.handleDisplayMessage(ctx, peer, label, env)
		return
	}
	if label == webrtc.DataChannelControl && env.Type == bark.TypeListPermissions {
		granted, gen := d.r.effectivePermissions()
		sendPermissions(peer, label, d.r.id, granted, gen)
		return
	}
	if label == webrtc.DataChannelTransfer && (env.Type == bark.TypeFileProgress || env.Type == bark.TypeFileSaved) {
		d.r.fileReply(from, env)
		return
	}
	if !isDataPlane(msgType) {
		return
	}
	var fields map[string]interface{}
	if err := json.Unmarshal(env.Payload, &fields); err != nil || fields == nil {
		return
	}
	agentID := d.r.id
	d.handle(ctx, from, msgType, fields, func(replyType string, replyFields map[string]interface{}) {
		data, err := bark.EncodeEnvelope(bark.MessageType(replyType), agentID, replyFields)
		if err != nil {
			return
		}
		if err := peer.SendData(label, data); err != nil {
			log.Printf("[AgentStream] could not reply on data channel %s: %v\n", label, err)
		}
	})
}

// RequireDataChannel keeps the user's data off the signalling socket, so the
// server in the middle never sees it. With it on, input, clipboard and file
// messages arriving over the socket are refused -- the viewer is told once per
// message type with {"type":"data_channel_required","message_type":...} --
// and the host's clipboard is sent only over data channels.
//
// Over a data channel, those messages are protected end to end by the DTLS
// session the offer and answer set up; with RequireSignedViewers on as well,
// the relay cannot substitute its own end of that session either.
//
// It is opt-in, because turning it on changes what every viewer must do: one
// that does not open the bark-control and bark-transfer data channels loses
// input, clipboard and file transfer. Without it, both paths are accepted, as
// they must be while viewers move over. The consent checks are the same
// either way.
//
// Call it before Serve.
func (r *Runner) RequireDataChannel(required bool) {
	r.requireDataChannel = required
}

// OmitFilePathInReply sets whether file_saved replies send only the base filename
// over the wire instead of the host's full local filesystem path (CWE-200).
func (r *Runner) OmitFilePathInReply(omit bool) {
	r.omitFilePathInReply = omit
}

// relayRefusal tells a viewer, once per message type and connection, that its
// data was refused because it came over the signalling socket.
type relayRefusal struct {
	mu   sync.Mutex
	told map[string]bool
}

func (rr *relayRefusal) refuse(msgType string, write func([]byte) error) {
	rr.mu.Lock()
	if rr.told[msgType] {
		rr.mu.Unlock()
		return
	}
	if rr.told == nil {
		rr.told = make(map[string]bool)
	}
	rr.told[msgType] = true
	rr.mu.Unlock()

	log.Printf("[AgentStream] Refusing %s over the signalling socket: a data channel is required\n", logSafe(msgType))
	notice, _ := json.Marshal(map[string]string{
		"type":         "data_channel_required",
		"message_type": msgType,
	})
	_ = write(notice)
}
