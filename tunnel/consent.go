// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import "log"

// The permissions a session can be granted, as named in
// bark.ConsentRequestPayload.RequestedPermissions. PermissionScreenView is
// recorded like the others but not enforced yet: the screen is captured as soon
// as a viewer negotiates.
const (
	PermissionScreenView    = "screen_view"
	PermissionRemoteControl = "remote_control"
	PermissionClipboard     = "clipboard"
	PermissionFileTransfer  = "file_transfer"
	// PermissionReverseStream covers reverse streams through the tunnel, such
	// as SSH. AgentStreamRunner never checks it itself: a reverse stream is
	// the TunnelClient's, and consults it only through
	// TunnelClient.ConsentReverseStream when a consumer wires that up.
	PermissionReverseStream = "reverse_stream"
)

// Grant records permissions the person at the machine has agreed to. It is for
// whoever shows the consent prompt -- a bark.ConsentRequestPayload arrives
// through Handle, the consumer asks the user, and calls Grant with what they
// accepted. Nothing else grants: a message arriving on the signalling socket
// never does, because the server is not who consents.
func (r *AgentStreamRunner) Grant(permissions ...string) {
	r.consentMu.Lock()
	defer r.consentMu.Unlock()
	if r.granted == nil {
		r.granted = make(map[string]bool)
	}
	for _, p := range permissions {
		r.granted[p] = true
		delete(r.warned, p)
	}
}

// Revoke withdraws the named permissions, or every permission if none are
// named. It also runs when the server closes the session.
func (r *AgentStreamRunner) Revoke(permissions ...string) {
	r.consentMu.Lock()
	defer r.consentMu.Unlock()
	if len(permissions) == 0 {
		r.granted = nil
		return
	}
	for _, p := range permissions {
		delete(r.granted, p)
	}
}

// Granted reports whether permission has been granted. Everything is denied
// until Grant says otherwise.
func (r *AgentStreamRunner) Granted(permission string) bool {
	r.consentMu.RLock()
	defer r.consentMu.RUnlock()
	return r.granted[permission]
}

// allow is Granted plus a log line when it refuses, so a session that does
// nothing because consent was never given is not a silent one. Input arrives
// dozens of times a second, so it logs once per permission until that
// permission is granted.
func (r *AgentStreamRunner) allow(permission, what string) bool {
	r.consentMu.Lock()
	defer r.consentMu.Unlock()
	if r.granted[permission] {
		return true
	}
	if !r.warned[permission] {
		if r.warned == nil {
			r.warned = make(map[string]bool)
		}
		r.warned[permission] = true
		log.Printf("[AgentStream] Refusing %s: %s has not been granted\n", what, permission)
	}
	return false
}
