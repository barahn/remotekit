// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sync"

	"github.com/barahn/remotekit/clipboard"
	"github.com/barahn/remotekit/transfer"
)

// clipBatch tracks files arriving together as a clipboard sync batch.
type clipBatch struct {
	batchID     string
	count       int
	cut         bool
	stagedFiles []string
	totalSize   int64
	fileNames   []string
}

// fileInbox receives files over a data channel: file_start, file_chunk and
// file_complete, answered with file_progress and file_saved. The agent
// receives what viewers send with one, and a viewer what the agent sends with
// another (barahn/remotekit#130), so both ends check, refuse and save files
// the same way.
type fileInbox struct {
	// transfer stores regular files; nil means this end cannot store any.
	transfer *transfer.Manager

	// clipTransfer stores files received via clipboard into the session staging folder.
	clipTransfer *transfer.Manager
	clipMgr      clipboard.Manager
	clipWatcher  *clipboard.Watcher
	onClipFiles  func(from sender, list clipboard.FileList, size int64, names []string)

	batchMu     sync.Mutex
	clipBatches map[string]*clipBatch
	clipTrans   map[string]string

	// omitPath, when set, returns only the base filename in file_saved replies
	// to prevent leaking the host's directory structure across the network (CWE-200).
	omitPath bool

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
		isClip, _ := fields["clipboard"].(bool)
		if isClip {
			if reason := gate("clipboard file transfer"); reason != "" {
				in.refuseFile(from, id, reason, reply)
				return
			}
			if in.clipTransfer == nil {
				in.refuseFile(from, id, "the "+self+" cannot stage clipboard files", reply)
				return
			}
			session := in.clipTransfer.StartSession(id, name, int64(size), sha)
			if session == nil {
				in.refuseFile(from, id, "the "+self+" rejected the clipboard file", reply)
				return
			}
			batchID, _ := fields["batch_id"].(string)
			batchCount, _ := fields["batch_count"].(float64)
			cut, _ := fields["cut"].(bool)
			if batchID == "" {
				batchID = id
			}
			if batchCount <= 0 {
				batchCount = 1
			}
			in.batchMu.Lock()
			if in.clipBatches == nil {
				in.clipBatches = make(map[string]*clipBatch)
				in.clipTrans = make(map[string]string)
			}
			b, ok := in.clipBatches[batchID]
			if !ok {
				b = &clipBatch{
					batchID: batchID,
					count:   int(batchCount),
					cut:     cut,
				}
				in.clipBatches[batchID] = b
			}
			b.totalSize += int64(size)
			b.fileNames = append(b.fileNames, session.FileName)
			in.clipTrans[id] = batchID
			in.batchMu.Unlock()
			log.Printf("[AgentStream] Started clipboard file transfer session %s (%s, %s bytes)\n", logSafe(id), logSafe(name), logSafe(fmt.Sprintf("%.0f", size)))
			return
		}

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
		if session == nil {
			in.refuseFile(from, id, "the "+self+" rejected the file: size limit or capacity reached", reply)
			return
		}
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
		in.batchMu.Lock()
		_, isClip := in.clipTrans[id]
		in.batchMu.Unlock()

		tm := in.transfer
		if isClip {
			tm = in.clipTransfer
		}
		if tm == nil {
			return
		}
		prog, done, err := tm.AddChunkBase64(id, int(idx), b64Data)
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
		in.batchMu.Lock()
		batchID, isClip := in.clipTrans[id]
		in.batchMu.Unlock()

		tm := in.transfer
		if isClip {
			tm = in.clipTransfer
		}
		if tm == nil {
			return
		}
		name, size := tm.Describe(id)
		destPath, sha, err := tm.AssembleFile(id)
		errStr := ""
		if err != nil {
			errStr = err.Error()
			log.Printf("[AgentStream] File assembly error for %s: %v\n", logSafe(id), err)
		} else {
			log.Printf("[AgentStream] File transfer %s completed! Saved to: %s (SHA: %s)\n", logSafe(id), logSafe(destPath), sha)
			if !isClip && saved != nil {
				saved(ReceivedFile{ViewerID: from.id, Key: from.key, TransferID: id, Name: name, Path: destPath, SHA256: sha, Size: size})
			}
		}

		if isClip && err == nil {
			in.batchMu.Lock()
			batch := in.clipBatches[batchID]
			var completedBatch *clipBatch
			if batch != nil {
				batch.stagedFiles = append(batch.stagedFiles, destPath)
				if len(batch.stagedFiles) == batch.count {
					completedBatch = batch
					delete(in.clipBatches, batchID)
				}
			}
			delete(in.clipTrans, id)
			in.batchMu.Unlock()

			if completedBatch != nil {
				fileList := clipboard.FileList{Files: completedBatch.stagedFiles, Cut: completedBatch.cut}
				if in.clipWatcher != nil {
					in.clipWatcher.UpdateLastFiles(fileList)
				}
				if in.clipMgr != nil {
					if fm, ok := in.clipMgr.(clipboard.FileManager); ok {
						_ = fm.SetFiles(context.Background(), fileList)
					}
				}
				if in.onClipFiles != nil {
					in.onClipFiles(from, fileList, completedBatch.totalSize, completedBatch.fileNames)
				}
			}
		}

		replyPath := destPath
		if in.omitPath && destPath != "" {
			replyPath = filepath.Base(destPath)
		}
		reply("file_saved", map[string]interface{}{
			"transfer_id": id,
			"path":        replyPath,
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
