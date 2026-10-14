// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"github.com/barahn/remotekit/bark"
	"github.com/barahn/remotekit/webrtc"
)

// A viewer is told what the person at the machine has granted, so that it can
// show what it may not do -- greyed-out controls, a line saying so -- rather
// than sending input that is dropped without a word. It asks with
// bark.TypeListPermissions when its control channel opens, and the agent
// answers with bark.TypePermissions, then sends one again whenever Grant,
// Revoke or SetStandingPermissions changes the set.
//
// The list is answered whatever has been granted, screen_view included: it
// names permissions, not anything of the machine's, and a viewer refused
// everything most needs to know.

// sendPermissions sends the granted permissions to peer on label.
func sendPermissions(peer *webrtc.PeerSession, label, agentID string, granted []string) {
	data, err := bark.EncodeEnvelope(bark.TypePermissions, agentID, bark.PermissionsPayload{Granted: granted})
	if err != nil {
		return
	}
	_ = peer.SendData(label, data)
}
