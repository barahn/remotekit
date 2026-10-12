// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/barahn/remotekit/bark"
	"github.com/barahn/remotekit/webrtc"
)

// The viewer's half of the data plane: clipboard both ways, and files from
// the viewer to the agent (barahn/remotekit#93). Everything goes over the data
// channels, inside the DTLS session, never over the signalling connection.

// viewerChunkSize is how much of a file one file_chunk carries. Base64 and the
// envelope add about 40%, and an SCTP message larger than 64 KB is refused by
// a peer that does not advertise more, so chunks stay well under it.
const viewerChunkSize = 16 * 1024

// fileWindow is how many chunks may be in flight before the agent has
// acknowledged one. The agent acknowledges every chunk, so this bounds what
// sits in the channel's buffers and makes the progress callback report what
// the agent actually has.
const fileWindow = 16

// ErrFileNotSaved is returned by SendFile when the agent did not save the
// file: the person at the machine has not granted file transfer, the file did
// not arrive intact, or the agent could not write it. The error carries the
// agent's reason.
var ErrFileNotSaved = errors.New("stream: the agent did not save the file")

// SavedFile is what the agent reports once a file sent with SendFile is on its
// disk.
type SavedFile struct {
	// Path is where the agent saved it, on the agent's machine.
	Path string
	// SHA256 is the hex SHA-256 of what the agent wrote; SendFile has
	// already checked it against what was sent.
	SHA256 string
}

// fileEvent is the agent's reply to one file message: an acknowledged chunk,
// or the final verdict.
type fileEvent struct {
	saved bool // file_saved; otherwise file_progress
	path  string
	hash  string
	err   string
}

// OnClipboard registers fn for the agent's clipboard: it is called with the
// text whenever the clipboard on the agent's machine changes, if the person at
// the machine granted clipboard access. It runs on the data channel's
// goroutine and must not block. Register it before Serve.
func (v *Viewer) OnClipboard(fn func(text string)) { v.onClipboard = fn }

// SendClipboard puts text on the agent's clipboard, over the control data
// channel. The agent applies it only if the person at the machine granted
// clipboard access; nothing reports a refusal, as with input.
func (v *Viewer) SendClipboard(text string) error {
	data, err := bark.EncodeEnvelope(bark.TypeClipboard, v.cfg.AgentID, map[string]string{"text": text})
	if err != nil {
		return err
	}
	return v.sendData(webrtc.DataChannelControl, data)
}

// SendFile sends the contents of r to the agent as a file named name, over the
// transfer data channel, and returns once the agent has saved it -- or refused
// it, or ctx ends. name is only a name: the agent drops any directory part.
//
// What is sent is r from its current position to the end. r is read twice,
// once to hash it and once to send it, so the agent can refuse a file that did
// not arrive intact; that is why it must seek. progress,
// if not nil, is called with the bytes the agent has acknowledged so far and
// the total, from SendFile's goroutine.
//
// It fails with ErrNotConnected while the transfer channel is not open, and
// with ErrFileNotSaved, carrying the agent's reason, if the agent did not save
// the file. A re-negotiation in the middle of a transfer fails it, and the
// caller sends it again.
func (v *Viewer) SendFile(ctx context.Context, name string, r io.ReadSeeker, progress func(sent, total int64)) (SavedFile, error) {
	// What is sent is what is left of r, from where it stands now.
	offset, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return SavedFile{}, err
	}
	hasher := sha256.New()
	size, err := io.Copy(hasher, r)
	if err != nil {
		return SavedFile{}, err
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	if _, err := r.Seek(offset, io.SeekStart); err != nil {
		return SavedFile{}, err
	}

	id, err := transferID()
	if err != nil {
		return SavedFile{}, err
	}
	events := make(chan fileEvent, fileWindow+2)
	v.mu.Lock()
	if v.transfers == nil {
		v.transfers = make(map[string]chan fileEvent)
	}
	v.transfers[id] = events
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		delete(v.transfers, id)
		v.mu.Unlock()
	}()

	send := func(t bark.MessageType, payload interface{}) error {
		data, err := bark.EncodeEnvelope(t, v.cfg.AgentID, payload)
		if err != nil {
			return err
		}
		return v.sendData(webrtc.DataChannelTransfer, data)
	}
	if err := send(bark.TypeFileStart, map[string]interface{}{
		"transfer_id": id,
		"name":        name,
		"size":        size,
		"sha256":      sum,
	}); err != nil {
		return SavedFile{}, err
	}

	// wait takes the agent's next reply. A file_saved before the end is a
	// refusal, and ends the transfer.
	acked := int64(0)
	wait := func() (*fileEvent, error) {
		select {
		case ev := <-events:
			if ev.saved {
				return &ev, nil
			}
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	buf := make([]byte, viewerChunkSize)
	sent, inFlight, index := int64(0), 0, 0
	for sent < size {
		for inFlight >= fileWindow {
			final, err := wait()
			if err != nil {
				return SavedFile{}, err
			}
			if final != nil {
				return SavedFile{}, fileVerdict(final, sum)
			}
			inFlight--
			acked = min(acked+viewerChunkSize, size)
			if progress != nil {
				progress(acked, size)
			}
		}
		n, err := io.ReadFull(r, buf[:min(int64(viewerChunkSize), size-sent)])
		if err != nil {
			return SavedFile{}, fmt.Errorf("stream: reading the file to send: %w", err)
		}
		if err := send(bark.TypeFileChunk, map[string]interface{}{
			"transfer_id": id,
			"index":       index,
			"data":        base64.StdEncoding.EncodeToString(buf[:n]),
		}); err != nil {
			return SavedFile{}, err
		}
		sent += int64(n)
		index++
		inFlight++
	}
	for inFlight > 0 {
		final, err := wait()
		if err != nil {
			return SavedFile{}, err
		}
		if final != nil {
			return SavedFile{}, fileVerdict(final, sum)
		}
		inFlight--
		acked = min(acked+viewerChunkSize, size)
		if progress != nil {
			progress(acked, size)
		}
	}

	if err := send(bark.TypeFileComplete, map[string]interface{}{"transfer_id": id}); err != nil {
		return SavedFile{}, err
	}
	for {
		final, err := wait()
		if err != nil {
			return SavedFile{}, err
		}
		if final == nil {
			continue
		}
		if err := fileVerdict(final, sum); err != nil {
			return SavedFile{}, err
		}
		if progress != nil && size == 0 {
			progress(0, 0)
		}
		return SavedFile{Path: final.path, SHA256: final.hash}, nil
	}
}

// fileVerdict turns the agent's file_saved into SendFile's result.
func fileVerdict(ev *fileEvent, sum string) error {
	switch {
	case ev.err != "":
		return fmt.Errorf("%w: %s", ErrFileNotSaved, ev.err)
	case ev.hash != sum:
		return fmt.Errorf("%w: it saved a file whose SHA-256 is %s, not %s", ErrFileNotSaved, ev.hash, sum)
	}
	return nil
}

func transferID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// sendData sends one envelope on a data channel of the current peer.
func (v *Viewer) sendData(label string, data []byte) error {
	v.mu.Lock()
	peer := v.peer
	v.mu.Unlock()
	if peer == nil || !peer.DataChannelOpen(label) {
		return ErrNotConnected
	}
	return peer.SendData(label, data)
}

// handleData acts on one envelope the agent sent over a data channel of peer.
// Messages from a peer that has since been replaced are dropped, and so are
// transfers that peer was carrying.
func (v *Viewer) handleData(peer *webrtc.PeerSession, msg []byte) {
	var env bark.Envelope
	if json.Unmarshal(msg, &env) != nil {
		return
	}
	v.mu.Lock()
	current := v.peer == peer
	v.mu.Unlock()
	if !current {
		return
	}
	switch env.Type {
	case bark.TypeClipboard:
		var p struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(env.Payload, &p) == nil && v.onClipboard != nil {
			v.onClipboard(p.Text)
		}
	case bark.TypeDisplays:
		var p bark.DisplaysPayload
		if json.Unmarshal(env.Payload, &p) == nil && v.onDisplays != nil {
			v.onDisplays(p)
		}
	case bark.TypeFileProgress, bark.TypeFileSaved:
		var p struct {
			ID     string `json:"transfer_id"`
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
			Error  string `json:"error"`
		}
		if json.Unmarshal(env.Payload, &p) != nil {
			return
		}
		v.mu.Lock()
		ch := v.transfers[p.ID]
		v.mu.Unlock()
		if ch == nil {
			return
		}
		ev := fileEvent{saved: env.Type == bark.TypeFileSaved, path: p.Path, hash: p.SHA256, err: p.Error}
		select {
		case ch <- ev:
		default:
			// More replies than chunks in flight: only a misbehaving agent
			// sends them, and SendFile is not waiting for them.
		}
	}
}

// failTransfersLocked ends every transfer in flight on a peer that is going away.
// Called with v.mu held.
func (v *Viewer) failTransfersLocked() {
	for id, ch := range v.transfers {
		select {
		case ch <- fileEvent{saved: true, err: "the connection to the agent was replaced"}:
		default:
		}
		delete(v.transfers, id)
	}
}

// OnDisplays registers fn for the agent's monitors: the list, which one is
// being streamed, and why a SelectDisplay failed, if it did. It is called
// once the control channel opens and whenever the monitor being streamed
// changes. It runs on the data channel's goroutine and must not block.
// Register it before Serve.
func (v *Viewer) OnDisplays(fn func(bark.DisplaysPayload)) { v.onDisplays = fn }

// SelectDisplay asks the agent to stream the monitor with the given index,
// from the list OnDisplays reported. The outcome arrives through OnDisplays:
// the new Active once its first frame is captured, or an Error.
func (v *Viewer) SelectDisplay(index int) error {
	data, err := bark.EncodeEnvelope(bark.TypeSelectDisplay, v.cfg.AgentID, bark.SelectDisplayPayload{Index: index})
	if err != nil {
		return err
	}
	return v.sendData(webrtc.DataChannelControl, data)
}

// requestDisplays asks the agent for its monitors, once the control channel
// of peer is open.
func (v *Viewer) requestDisplays(peer *webrtc.PeerSession, label string) {
	if label != webrtc.DataChannelControl {
		return
	}
	data, err := bark.EncodeEnvelope(bark.TypeListDisplays, v.cfg.AgentID, struct{}{})
	if err != nil {
		return
	}
	_ = peer.SendData(label, data)
}
