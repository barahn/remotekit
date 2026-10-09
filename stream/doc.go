// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

// Package stream serves the agent side of a remote session over any
// signalling transport: WebRTC negotiation, the screen, and the data plane
// (input, clipboard, file transfer), each gated by the consent of the person
// at the machine.
//
// It is the session half of what package tunnel used to hold alone, split out
// so a consumer with no enrolment and no reverse tunnel -- an on-demand support
// tool, say -- can run a session without importing tunnel or its yamux
// dependency. A Runner is built from an explicit identity (Config): a session
// ID and, optionally, an in-memory ed25519 key its answers are signed with.
// No credentials file, no device key file and no control plane are involved.
//
//	r := stream.New(stream.Config{ID: sessionID, SigningKey: key, PeerConfig: &ice})
//	r.RequireScreenViewConsent(true)
//	r.RequireSignedViewers(trusted)
//	// ... ask the person at the machine, then:
//	r.Grant(stream.PermissionScreenView, stream.PermissionRemoteControl)
//	err := r.Serve(ctx, conn) // conn is any SignalConn
//
// PeerConfig carries the deployment's own STUN/TURN servers; left nil, the
// runner falls back to webrtc.DefaultPeerConfig(), which uses public STUN.
//
// Everything is denied until granted. The checks that make an untrusted
// transport safe -- RequireSignedViewers, RequireScreenViewConsent,
// RequireDataChannel, DisableRelayFallback -- are opt-in, and a deployment
// whose signalling path it does not control wants all of them on.
//
// Package tunnel's AgentStreamRunner wraps a Runner with an enrolled agent's
// credentials and the control plane's WebSocket, and is unchanged for its
// callers.
package stream
