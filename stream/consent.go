// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import "log"

// The permissions a session can be granted, as named in
// bark.ConsentRequestPayload.RequestedPermissions. PermissionScreenView is
// enforced only when the consumer opts in with RequireScreenViewConsent;
// otherwise the screen is captured as soon as a viewer negotiates, as before.
const (
	PermissionScreenView    = "screen_view"
	PermissionRemoteControl = "remote_control"
	PermissionClipboard     = "clipboard"
	PermissionFileTransfer  = "file_transfer"
	// PermissionReverseStream covers reverse streams through package
	// tunnel, such as SSH. The Runner never checks it itself: a reverse
	// stream is tunnel.TunnelClient's, and consults it only through
	// TunnelClient.ConsentReverseStream when a consumer wires that up to
	// Granted.
	PermissionReverseStream = "reverse_stream"
)

// Grant records permissions the person at the machine has agreed to. It is for
// whoever shows the consent prompt -- a bark.ConsentRequestPayload arrives
// through Handle, the consumer asks the user, and calls Grant with what they
// accepted. Nothing on the signalling socket grants, because the server is not
// who consents; the only other source of permission is the consumer's own
// policy, through SetStandingPermissions.
func (r *Runner) Grant(permissions ...string) {
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
// named. It also runs when the server closes the session. It withdraws only
// what Grant gave; standing permissions are changed with
// SetStandingPermissions.
func (r *Runner) Revoke(permissions ...string) {
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

// SetStandingPermissions replaces the permissions the consumer's own policy
// allows without asking anyone; call it with none to clear them. Unlike Grant,
// they hold across sessions: a close does not revoke them, and neither does
// Revoke.
//
// It is for hosts where nobody is at the machine to consent, as on an
// unattended fleet host, and where enrolling the machine is the decision that
// lets an operator in. Making that call is the consumer's, which is why the
// default stays deny: a consumer that wants a standing permission names it.
// Where somebody is at the machine to ask, ask them and use Grant instead.
func (r *Runner) SetStandingPermissions(permissions ...string) {
	r.consentMu.Lock()
	defer r.consentMu.Unlock()
	r.standing = nil
	for _, p := range permissions {
		if r.standing == nil {
			r.standing = make(map[string]bool)
		}
		r.standing[p] = true
		delete(r.warned, p)
	}
}

// Granted reports whether permission has been granted, by Grant or as a
// standing permission. Everything is denied until one of them says otherwise.
func (r *Runner) Granted(permission string) bool {
	r.consentMu.RLock()
	defer r.consentMu.RUnlock()
	return r.granted[permission] || r.standing[permission]
}

// allow is Granted plus a log line when it refuses, so a session that does
// nothing because consent was never given is not a silent one. Input arrives
// dozens of times a second, so it logs once per permission until that
// permission is granted.
func (r *Runner) allow(permission, what string) bool {
	r.consentMu.Lock()
	defer r.consentMu.Unlock()
	if r.granted[permission] || r.standing[permission] {
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

// RequireScreenViewConsent makes the screen subject to consent like input,
// clipboard and files: a viewer's offer or session_start is refused until
// PermissionScreenView is granted, and frames stop the moment it is revoked.
// A refused viewer is sent {"type":"consent_required","permission":"screen_view"}
// so it can say why nothing arrives, rather than wait on a negotiation the
// agent will not answer.
//
// It is opt-in, because turning it on changes what every consumer must do:
// one that does not Grant PermissionScreenView would stop showing the screen.
// Without it, capture starts as soon as a viewer negotiates, as it always has.
// Where somebody is at the machine to ask, turn it on and Grant screen_view
// with the rest of what they accept; where nobody is, as on an unattended
// fleet host, leave it off.
//
// Call it before Serve.
func (r *Runner) RequireScreenViewConsent(required bool) {
	r.requireScreenView = required
}
