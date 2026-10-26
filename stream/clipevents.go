// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import "crypto/ed25519"

// The clipboard as the person at the machine should see it (barahn/remotekit#130).
// Once PermissionClipboard is granted the clipboard syncs on its own, both
// ways; a consumer that tells that person what moves -- every paste into
// their machine, every copy that leaves it -- registers OnClipboardEvent.
// The events carry sizes, never the text.

// ClipboardDirection is which way a clipboard event went.
type ClipboardDirection int

const (
	// ClipboardToHost is a viewer's text applied to the host's clipboard:
	// something pasted into the machine.
	ClipboardToHost ClipboardDirection = iota + 1
	// ClipboardFromHost is the host's clipboard sent to the viewers: what
	// the person at the machine copied, leaving it. With Concealed set it
	// was held back instead.
	ClipboardFromHost
)

// MaxClipboardFileSize is the maximum total size of files (25 MB) transferred
// automatically through the clipboard (barahn/remotekit#131).
const MaxClipboardFileSize = 25 * 1024 * 1024

// ClipboardEvent is one clipboard sync, or one held back.
type ClipboardEvent struct {
	Direction ClipboardDirection
	// ViewerID and Key are the viewer whose text or files were applied, for
	// ClipboardToHost, as in FileOffer. Empty for ClipboardFromHost, which
	// goes to every viewer.
	ViewerID string
	Key      ed25519.PublicKey
	// Size is the text's length or total files size in bytes.
	Size int
	// Concealed is a host copy a password manager marked as secret, which
	// was not sent (clipboard.Concealer).
	Concealed bool
	// Files is the list of filenames copied in a file clipboard event.
	Files []string
	// TooLarge is set when copied files total more than MaxClipboardFileSize
	// and are reported locally without being sent across the wire.
	TooLarge bool
}

// OnClipboardEvent registers fn for every clipboard sync the runner makes
// while PermissionClipboard is granted. It runs on the runner's goroutines
// and must not block.
//
// Call it before Serve.
func (r *Runner) OnClipboardEvent(fn func(ClipboardEvent)) { r.onClipboardEvent = fn }

func (r *Runner) clipboardEvent(ev ClipboardEvent) {
	if r.onClipboardEvent != nil {
		r.onClipboardEvent(ev)
	}
}
