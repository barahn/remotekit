// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build darwin

package permission

import "github.com/barahn/remotekit/internal/macos"

// Check reports whether the running process holds p, without asking anyone.
//
// Screen Recording is read with CGPreflightScreenCaptureAccess, whose answer
// is fixed for the life of the process: a grant made in System Settings while
// the process runs still reads NotGranted until the process is relaunched
// (observed on macOS 15.8). ScreenCaptureKit itself would already capture,
// but screen's capturer does not ask it without this preflight, because every
// ScreenCaptureKit call made without the permission raises the system's
// dialog again. So: grant, then relaunch.
//
// Accessibility is read with AXIsProcessTrusted, which follows System Settings
// as it changes; no relaunch is needed.
func Check(p Permission) Status {
	if macos.Load() != nil {
		return Unknown
	}
	switch p {
	case ScreenRecording:
		return status(macos.CGPreflightScreenCaptureAccess())
	case Accessibility:
		return status(macos.AXIsProcessTrusted())
	}
	return Unknown
}

// Request asks the system to request p from the person, then reports p's
// status as Check does.
//
// For Screen Recording that is CGRequestScreenCaptureAccess, which shows its
// dialog only the first time a given application asks; after that it returns
// at once. For Accessibility it is AXIsProcessTrustedWithOptions with the
// prompt option, which shows the system's dialog pointing at System Settings.
// In both cases the grant itself happens in System Settings, so Request
// returns NotGranted until the person has gone there.
func Request(p Permission) Status {
	if macos.Load() != nil {
		return Unknown
	}
	switch p {
	case ScreenRecording:
		return status(macos.CGRequestScreenCaptureAccess())
	case Accessibility:
		var trusted bool
		macos.WithAutoreleasePool(func() {
			yes := macos.Class("NSNumber").Send(macos.Sel("numberWithBool:"), true)
			// kAXTrustedCheckOptionPrompt's value. A CFStringRef and an
			// NSString are the same object, so the dictionary is passed as is.
			key := macos.NSString("AXTrustedCheckOptionPrompt")
			opts := macos.Class("NSDictionary").Send(macos.Sel("dictionaryWithObject:forKey:"), yes, key)
			trusted = macos.AXIsProcessTrustedWithOptions(opts)
		})
		return status(trusted)
	}
	return Unknown
}

func status(held bool) Status {
	if held {
		return Granted
	}
	return NotGranted
}
