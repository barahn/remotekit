// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"

	"github.com/barahn/remotekit/bark"
	"github.com/barahn/remotekit/transfer"
	"github.com/barahn/remotekit/webrtc"
)

// Files from the agent to a viewer (barahn/remotekit#130): the same protocol
// as a viewer's SendFile, the other way round. The person at the machine
// still decides whether files move at all (PermissionFileTransfer), and the
// viewer's consumer decides on each file (Viewer.OnFileOffer). Files go over
// the viewer's transfer data channel only, never the signalling path.

// ErrNotGranted is returned by Runner.SendFile when the person at the machine
// has not granted what it needs.
var ErrNotGranted = errors.New("stream: the permission has not been granted")

// outgoingFile is one Runner.SendFile in flight: the viewer it goes to, and
// where that viewer's replies are delivered.
type outgoingFile struct {
	viewerID string
	events   chan fileEvent
}

// SendFile sends what is left of r to the viewer viewerID as a file named
// name, and returns once the viewer has saved it -- or refused it, or ctx
// ends. It is Viewer.SendFile from the agent's side: r is read twice, so it
// must seek, and progress, if not nil, hears the bytes the viewer has
// acknowledged.
//
// It needs PermissionFileTransfer, checked before each chunk, so taking the
// permission back stops a transfer under way (ErrNotGranted). It fails with
// ErrNotConnected when viewerID has no transfer channel open, and with
// ErrFileNotSaved, carrying the viewer's reason, when the viewer did not
// save the file. A viewer that goes away mid-transfer leaves SendFile
// waiting until ctx ends.
func (r *Runner) SendFile(ctx context.Context, viewerID, name string, rd io.ReadSeeker, progress func(sent, total int64)) (SavedFile, error) {
	if !r.allow(PermissionFileTransfer, "file send") {
		return SavedFile{}, ErrNotGranted
	}
	peer := r.viewerPeer(viewerID)
	if peer == nil || !peer.DataChannelOpen(webrtc.DataChannelTransfer) {
		return SavedFile{}, ErrNotConnected
	}

	id, err := transferID()
	if err != nil {
		return SavedFile{}, err
	}
	events := make(chan fileEvent, fileWindow+2)
	r.outgoingMu.Lock()
	if r.outgoing == nil {
		r.outgoing = make(map[string]outgoingFile)
	}
	r.outgoing[id] = outgoingFile{viewerID: viewerID, events: events}
	r.outgoingMu.Unlock()
	defer func() {
		r.outgoingMu.Lock()
		delete(r.outgoing, id)
		r.outgoingMu.Unlock()
	}()

	send := func(t bark.MessageType, payload interface{}) error {
		if !r.allow(PermissionFileTransfer, "file send") {
			return ErrNotGranted
		}
		data, err := bark.EncodeEnvelope(t, r.id, payload)
		if err != nil {
			return err
		}
		if !peer.DataChannelOpen(webrtc.DataChannelTransfer) {
			return ErrNotConnected
		}
		return peer.SendData(webrtc.DataChannelTransfer, data)
	}
	return sendFileOver(ctx, id, name, rd, progress, send, events)
}

// viewerPeer is the peer connection of viewerID in whichever Serve has it.
func (r *Runner) viewerPeer(viewerID string) *webrtc.PeerSession {
	r.sessionsMu.Lock()
	hooks := make([]*sessionHook, 0, len(r.sessions))
	for h := range r.sessions {
		hooks = append(hooks, h)
	}
	r.sessionsMu.Unlock()
	for _, h := range hooks {
		if h.peer == nil {
			continue
		}
		if p := h.peer(viewerID); p != nil {
			return p
		}
	}
	return nil
}

// fileReply delivers a viewer's file_progress or file_saved to the SendFile
// waiting for it. A reply from any other viewer than the one the file went
// to is dropped.
func (r *Runner) fileReply(from sender, env bark.Envelope) {
	var p struct {
		ID     string `json:"transfer_id"`
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Error  string `json:"error"`
	}
	if json.Unmarshal(env.Payload, &p) != nil {
		return
	}
	r.outgoingMu.Lock()
	out, ok := r.outgoing[p.ID]
	r.outgoingMu.Unlock()
	if !ok || out.viewerID != from.id {
		return
	}
	ev := fileEvent{saved: env.Type == bark.TypeFileSaved, path: p.Path, hash: p.SHA256, err: p.Error}
	select {
	case out.events <- ev:
	default: // more replies than chunks in flight: SendFile is not waiting for them
	}
}

// OnFileOffer registers fn to decide on each file the agent starts sending,
// before any of it is written; returning false refuses it. Without it the
// viewer refuses every file: taking files from the agent is the consumer's
// choice. The offer's ViewerID is the agent's ID and its Key the agent's key
// (ViewerConfig.AgentKey), if one is pinned. fn runs on the data channel's
// goroutine and is waited for.
//
// Call it before Serve.
func (v *Viewer) OnFileOffer(fn func(FileOffer) bool) { v.onFileOffer = fn }

// OnFileSaved registers fn to be told of every file from the agent saved to
// ViewerConfig.DownloadDir, with its path. It runs on the data channel's
// goroutine and must not block.
//
// Call it before Serve.
func (v *Viewer) OnFileSaved(fn func(ReceivedFile)) { v.onFileSaved = fn }

// receiveFile acts on one file message the agent sent over peer's transfer
// channel, answering on that channel.
func (v *Viewer) receiveFile(peer *webrtc.PeerSession, env bark.Envelope) {
	var fields map[string]interface{}
	if json.Unmarshal(env.Payload, &fields) != nil || fields == nil {
		return
	}
	v.inboxOnce.Do(func() {
		m, err := transfer.NewManager(v.cfg.DownloadDir)
		if err != nil {
			log.Printf("[Viewer] cannot store files from the agent: %v\n", err)
			return
		}
		v.inbox.transfer = m
		v.inbox.omitPath = v.cfg.OmitFilePathInReply
	})
	agentID := v.cfg.AgentID
	reply := func(replyType string, replyFields map[string]interface{}) {
		data, err := bark.EncodeEnvelope(bark.MessageType(replyType), v.cfg.ID, replyFields)
		if err != nil {
			return
		}
		_ = peer.SendData(webrtc.DataChannelTransfer, data)
	}
	v.inbox.handle(sender{id: agentID, key: v.cfg.AgentKey}, string(env.Type), fields, "viewer",
		func(string) string { return "" }, v.onFileOffer, false, v.onFileSaved, reply)
}
