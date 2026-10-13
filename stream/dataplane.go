// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/barahn/remotekit/bark"
	"github.com/barahn/remotekit/clipboard"
	"github.com/barahn/remotekit/transfer"
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
	transfer    *transfer.Manager

	// displays and selectDisplay reach the session's capturer; see
	// displays.go. Nil outside a session.
	displays      func() (bark.DisplaysPayload, bool)
	selectDisplay func(index int) error

	// refused holds the transfers already told they were refused, by
	// refusalKey; see refuseFile.
	refusedMu sync.Mutex
	refused   map[[sha256.Size]byte]struct{}
}

// maxRefused bounds refused. A transfer that outlives it is told again, once.
const maxRefused = 256

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
		}

	case "file_start":
		id, _ := fields["transfer_id"].(string)
		name, _ := fields["name"].(string)
		size, _ := fields["size"].(float64)
		sha, _ := fields["sha256"].(string)
		if id != "" && !r.allow(PermissionFileTransfer, "file transfer") {
			d.refuseFile(from, id, PermissionFileTransfer+" has not been granted", reply)
			return
		}
		if id != "" && d.transfer == nil {
			d.refuseFile(from, id, "the agent cannot store files", reply)
			return
		}
		if id != "" {
			// An empty name is saved under transfer's fallback name.
			session := d.transfer.StartSession(id, name, int64(size), sha)
			if r.onFileOffer != nil && !r.onFileOffer(FileOffer{ViewerID: from.id, Key: from.key, TransferID: id, Name: session.FileName, Size: int64(size)}) {
				d.transfer.Discard(id)
				d.refuseFile(from, id, "the agent refused the file", reply)
				return
			}
			log.Printf("[AgentStream] Started file transfer session %s (%s, %s bytes)\n", logSafe(id), logSafe(name), logSafe(fmt.Sprintf("%.0f", size)))
		}

	case "file_chunk":
		id, _ := fields["transfer_id"].(string)
		idx, _ := fields["index"].(float64)
		b64Data, _ := fields["data"].(string)
		if id != "" && !r.allow(PermissionFileTransfer, "file chunk") {
			d.refuseFile(from, id, PermissionFileTransfer+" has not been granted", reply)
			return
		}
		if id != "" && d.transfer != nil {
			prog, done, err := d.transfer.AddChunkBase64(id, int(idx), b64Data)
			if errors.Is(err, transfer.ErrTransferNotFound) {
				// Refused at file_start, or never started: refuse it
				// rather than acknowledge data that is dropped.
				d.refuseFile(from, id, "no such transfer", reply)
				return
			}
			if err != nil {
				log.Printf("[AgentStream] File chunk error: %v\n", err)
			}
			reply("file_progress", map[string]interface{}{
				"transfer_id": id,
				"progress":    prog,
				"done":        done,
			})
		}

	case "file_complete":
		id, _ := fields["transfer_id"].(string)
		if id != "" && !r.allow(PermissionFileTransfer, "file save") {
			d.refuseFile(from, id, PermissionFileTransfer+" has not been granted", reply)
			return
		}
		if id != "" && d.transfer != nil {
			name, size := d.transfer.Describe(id)
			destPath, sha, err := d.transfer.AssembleFile(id)
			errStr := ""
			if err != nil {
				errStr = err.Error()
				log.Printf("[AgentStream] File assembly error for %s: %v\n", logSafe(id), err)
			} else {
				log.Printf("[AgentStream] File transfer %s completed! Saved to: %s (SHA: %s)\n", logSafe(id), logSafe(destPath), sha)
				if r.onFileSaved != nil {
					r.onFileSaved(ReceivedFile{ViewerID: from.id, Key: from.key, TransferID: id, Name: name, Path: destPath, SHA256: sha, Size: size})
				}
			}
			reply("file_saved", map[string]interface{}{
				"transfer_id": id,
				"path":        destPath,
				"sha256":      sha,
				"error":       errStr,
			})
		}
	}
}

// refuseFile tells the sender a file message was refused, and why, with the
// file_saved it would otherwise wait for, so it stops sending instead of
// waiting for replies that will never come.
//
// It says so once per transfer. A sender that does not wait for each chunk's
// reply has a whole file's chunks in flight by the time the refusal reaches
// it, and one file_saved per chunk would be one error per chunk. A transfer
// is its sender and its ID together, so one viewer reusing another's ID
// cannot silence the refusal the other is owed.
func (d *dataPlane) refuseFile(from sender, id, reason string, reply replyFunc) {
	key := refusalKey(from, id)
	d.refusedMu.Lock()
	_, told := d.refused[key]
	if !told {
		if d.refused == nil || len(d.refused) >= maxRefused {
			d.refused = make(map[[sha256.Size]byte]struct{})
		}
		d.refused[key] = struct{}{}
	}
	d.refusedMu.Unlock()
	if told {
		return
	}
	reply("file_saved", map[string]interface{}{
		"transfer_id": id,
		"error":       reason,
	})
}

// refusalKey identifies a transfer in refused. It is a hash, not the IDs
// themselves, so the record stays maxRefused small hashes however long the
// IDs a sender makes up; each field is length-prefixed so no two different
// transfers share one.
func refusalKey(from sender, id string) [sha256.Size]byte {
	h := sha256.New()
	for _, field := range [][]byte{[]byte(from.id), from.key, []byte(id)} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(field)))
		h.Write(n[:])
		h.Write(field)
	}
	var key [sha256.Size]byte
	h.Sum(key[:0])
	return key
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
