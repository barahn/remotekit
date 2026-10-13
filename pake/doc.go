// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

// Package pake lets two parties who share a short secret -- a code read out
// over the phone -- learn each other's ed25519 public key over a carrier
// they do not trust, such as a signalling server or Nostr relays
// (barahn/remotekit#96).
//
// Signing every signal (webrtc.SignalMessage.Sign) demotes the carrier only
// if each end already knows the other's key by a route the carrier does not
// control. Two people meeting for the first time have no such route, and a
// carrier that relays their public keys can swap them for its own. This
// package is that route: a password-authenticated key exchange over the
// shared secret, which binds the two keys.
//
// # The protocol
//
// It is CPace (draft-irtf-cfrg-cpace-21) in its CPACE-RISTRETTO255-SHA512
// cipher suite, in the initiator-responder setting, with the explicit key
// confirmation of the draft's section 10.4, from github.com/mendsec/cpace.
// Each end's ed25519 public key is its associated data (ADa, ADb), so the
// transcript both ends hash binds both keys, and the confirmation tags prove
// it:
//
//	initiator                                  responder
//	NewInitiator  -- start:   Ya, ADa  -->     NewResponder
//	              <-- reply:  Yb, ADb, Tb --
//	Finish        -- confirm: Ta       -->     Finish
//
// The messages are opaque bytes; the caller carries them however it likes.
// The secret, the Context and the SessionID never travel.
//
// What it guarantees: an end that finishes knows the other end's ed25519
// key, and that whoever holds it typed the same secret. Someone in the middle
// who does not know the secret gets one guess per attempt, learns nothing it
// can test offline, and makes the exchange fail on both ends -- which a user
// sees. With a 4-digit secret that is one chance in 10,000 per attempt, so
// attempts must be limited and each failure shown.
//
// What it does not do: say who the other person is. The secret proves only
// that both ends have it.
//
// # Where CPace comes from
//
// CPace itself is github.com/mendsec/cpace, which implements the draft and is
// checked byte for byte against its test vectors. The Go implementations
// surveyed for barahn/chirp#21 do not follow the current draft
// (barahn/remotekit#96). This package adds only what binding keys needs:
// each end's ed25519 public key as its associated data, and HKDF over ISK.
package pake
