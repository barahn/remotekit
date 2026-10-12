// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

// Files a viewer sends to the agent (barahn/remotekit#98): where they land,
// whether they are accepted, and who is told once they are on disk. Consent
// (PermissionFileTransfer) still comes first; these are the consumer's own
// rules on top of it.

// FileOffer is a file a viewer is about to send.
type FileOffer struct {
	// ViewerID is the viewer sending it.
	ViewerID string
	// TransferID identifies the transfer in the viewer's messages.
	TransferID string
	// Name is the file's name, without any directory part.
	Name string
	// Size is the size the viewer announced, in bytes.
	Size int64
}

// ReceivedFile is a file a viewer sent, once it is on the agent's disk.
type ReceivedFile struct {
	ViewerID   string
	TransferID string
	// Name is the name the viewer gave it; Path is where it was saved, which
	// differs when a file of that name was already there.
	Name   string
	Path   string
	SHA256 string
	Size   int64
}

// OnFileOffer registers fn to decide whether to accept each file a viewer
// starts sending, before any of it is written. Returning false refuses it:
// nothing reaches the disk, and the viewer is told. Without it every file
// is accepted, as far as consent allows. fn runs on the data channel's
// goroutine and is waited for, so it should answer quickly; a consumer that
// asks the person at the machine should have asked beforehand.
//
// Call it before Serve.
func (r *Runner) OnFileOffer(fn func(FileOffer) bool) { r.onFileOffer = fn }

// OnFileSaved registers fn to be told of every file saved to disk, with the
// path it was saved at. It runs on the data channel's goroutine and must not
// block.
//
// Call it before Serve.
func (r *Runner) OnFileSaved(fn func(ReceivedFile)) { r.onFileSaved = fn }
