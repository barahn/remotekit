// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/barahn/remotekit/transfer"
)

// fileInbox receives files over a data channel: file_start, file_chunk and
// file_complete, answered with file_progress and file_saved. The agent
// receives what viewers send with one, and a viewer what the agent sends with
// another (barahn/remotekit#130), so both ends check, refuse and save files
// the same way.
type fileInbox struct {
	// transfer stores the files; nil means this end cannot store any.
	transfer *transfer.Manager

	// refused holds the transfers already told they were refused, by
	// refusalKey; see refuseFile.
	refusedMu sync.Mutex
	refused   map[[sha256.Size]byte]struct{}
}

// maxRefused bounds refused. A transfer that outlives it is told again, once.
const maxRefused = 256

// handle acts on one file message from from. self names this end in refusals
// ("agent" or "viewer"). gate is asked at each stage -- "file transfer",
// "file chunk", "file save" -- and returns why the file is refused, or "".
// offer decides on a new file before anything is written; with offer nil,
// acceptUnasked decides. saved hears of every file saved.
func (in *fileInbox) handle(from sender, msgType string, fields map[string]interface{}, self string, gate func(stage string) string, offer func(FileOffer) bool, acceptUnasked bool, saved func(ReceivedFile), reply replyFunc) {
	id, _ := fields["transfer_id"].(string)
	if id == "" {
		return
	}
	switch msgType {
	case "file_start":
		name, _ := fields["name"].(string)
		size, _ := fields["size"].(float64)
		sha, _ := fields["sha256"].(string)
		if reason := gate("file transfer"); reason != "" {
			in.refuseFile(from, id, reason, reply)
			return
		}
		if in.transfer == nil {
			in.refuseFile(from, id, "the "+self+" cannot store files", reply)
			return
		}
		if offer == nil && !acceptUnasked {
			in.refuseFile(from, id, "the "+self+" does not take files", reply)
			return
		}
		// An empty name is saved under transfer's fallback name.
		session := in.transfer.StartSession(id, name, int64(size), sha)
		if offer != nil && !offer(FileOffer{ViewerID: from.id, Key: from.key, TransferID: id, Name: session.FileName, Size: int64(size)}) {
			in.transfer.Discard(id)
			in.refuseFile(from, id, "the "+self+" refused the file", reply)
			return
		}
		log.Printf("[AgentStream] Started file transfer session %s (%s, %s bytes)\n", logSafe(id), logSafe(name), logSafe(fmt.Sprintf("%.0f", size)))

	case "file_chunk":
		idx, _ := fields["index"].(float64)
		b64Data, _ := fields["data"].(string)
		if reason := gate("file chunk"); reason != "" {
			in.refuseFile(from, id, reason, reply)
			return
		}
		if in.transfer == nil {
			return
		}
		prog, done, err := in.transfer.AddChunkBase64(id, int(idx), b64Data)
		if errors.Is(err, transfer.ErrTransferNotFound) {
			// Refused at file_start, or never started: refuse it rather
			// than acknowledge data that is dropped.
			in.refuseFile(from, id, "no such transfer", reply)
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

	case "file_complete":
		if reason := gate("file save"); reason != "" {
			in.refuseFile(from, id, reason, reply)
			return
		}
		if in.transfer == nil {
			return
		}
		name, size := in.transfer.Describe(id)
		destPath, sha, err := in.transfer.AssembleFile(id)
		errStr := ""
		if err != nil {
			errStr = err.Error()
			log.Printf("[AgentStream] File assembly error for %s: %v\n", logSafe(id), err)
		} else {
			log.Printf("[AgentStream] File transfer %s completed! Saved to: %s (SHA: %s)\n", logSafe(id), logSafe(destPath), sha)
			if saved != nil {
				saved(ReceivedFile{ViewerID: from.id, Key: from.key, TransferID: id, Name: name, Path: destPath, SHA256: sha, Size: size})
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

// refuseFile tells the sender a file message was refused, and why, with the
// file_saved it would otherwise wait for, so it stops sending instead of
// waiting for replies that will never come.
//
// It says so once per transfer. A sender that does not wait for each chunk's
// reply has a whole file's chunks in flight by the time the refusal reaches
// it, and one file_saved per chunk would be one error per chunk. A transfer
// is its sender and its ID together, so one viewer reusing another's ID
// cannot silence the refusal the other is owed.
func (in *fileInbox) refuseFile(from sender, id, reason string, reply replyFunc) {
	key := refusalKey(from, id)
	in.refusedMu.Lock()
	_, told := in.refused[key]
	if !told {
		if in.refused == nil || len(in.refused) >= maxRefused {
			in.refused = make(map[[sha256.Size]byte]struct{})
		}
		in.refused[key] = struct{}{}
	}
	in.refusedMu.Unlock()
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
