// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

// Package nostr is a signalling transport over Nostr relays: a
// tunnel.SignalConn whose messages travel as gift-wrapped events instead of
// over the control plane's WebSocket.
//
// It is its own module so that the remotekit core never imports it, or the
// secp256k1 code it needs. Nothing in the core knows it exists; a consumer
// that wants it hands a *Conn to tunnel.AgentStreamRunner.Serve.
//
// # What the relays see
//
// Each message is a NIP-17-style gift wrap (NIP-59): the message is the
// content of an unsigned rumor, sealed (kind 13) and signed by the sender's
// key, encrypted with NIP-44 to the recipient, and wrapped again (kind 1059)
// under a one-time key. A relay learns the recipient's public key, from the
// wrap's "p" tag, and the size and timing of the traffic. It does not learn
// the sender or the content. Every wrap carries a NIP-40 expiration, so a
// relay that honours it deletes the event once the session no longer needs it.
//
// The rumor is of kind [KindSignal], a kind of our own: NIP-AC is a proposal,
// not a standard, and the kind is only ever seen by the two ends.
//
// # Keys
//
// The key a Conn is addressed by should be generated for the session and
// thrown away after it, which is what [Listen] and [Dial] do when
// [Config.Key] is nil. A key that outlives sessions is a permanent, public
// marker of the device on every relay it touches; an operator's personal
// Nostr identity must never be used here.
//
// The Nostr key addresses; it does not authenticate. Who a viewer is, and
// whether the agent is who it says, is settled the same way as over the
// WebSocket: by the Ed25519 signatures on the signalling messages themselves
// (tunnel.AgentStreamRunner.RequireSignedViewers) and by consent. A relay is
// exactly as untrusted as the control plane, and those checks should be on.
//
// # Rendezvous
//
// The viewer must learn the agent's session public key and relay list some
// other way -- a support code read out over the phone, a QR code, a link.
// That is the product's to design; this package only needs the key and the
// relays.
//
// # Limits
//
// A message must fit a NIP-44 payload after being wrapped twice: about 27 KB
// for JSON heavy with escapes, such as SDP, and up to about 40 KB for plain
// text ([ErrMessageTooLarge] otherwise). SDP and candidates fit
// easily; the JPEG relay fallback does not, and should be refused with
// tunnel.AgentStreamRunner.DisableRelayFallback, with the data plane kept off
// the relays by tunnel.AgentStreamRunner.RequireDataChannel. Relays are not
// redialled: a Conn ends when its last relay does, and the caller dials again.
package nostr
