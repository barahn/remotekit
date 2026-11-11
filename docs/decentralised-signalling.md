# Decentralised signalling and an untrusted control plane

**Status:** Adopted — Phases 0, 1, and 2 implemented as designed (with `stream.SignalConn` seam in Phase 2). Phase 3 in progress.  
**Implementation Roadmap:** [implementation-phases.md](implementation-phases.md)  
**Scope:** `webrtc`, `stream`, `tunnel`, `signal/nostr`, `bark`. Policy consequences for Chirp and the
Barahn platform are noted but not decided here.

## The question this answers

Can a Nostr-style relay network replace our control plane for discovery and
signalling, the way trackers and DHT do for BitTorrent, and would that make our
remote access products decentralised and secure?

Yes to the first, with caveats. No to the second, on its own — and that second
answer is the point of this document.

## 1. Zero-Trust Signalling Objectives

**Decentralisation and cryptographic authorization are two separate axes, with end-to-end security established independently of transport relays.**

In a zero-trust model, the control plane is treated as an untrusted transport:

| Security Goal | Implementation Mechanism |
|---|---|
| End-to-end device identity | Cryptographic pairing via ed25519 identity keys |
| Direct encrypted media | WebRTC SRTP/DTLS transport with mutual key verification |
| Direct secure data channels | Encrypted WebRTC DataChannels for input, clipboard, and file transfer |
| Local port access restriction | Explicit port forwarding allowlists and strict policy enforcement |

> The architectural priority is: **establish untrusted, zero-knowledge signalling transport.** Once endpoints authenticate and encrypt directly to each other, the intermediary network path does not hold decryption capabilities. Transport decentralisation becomes a deployment choice rather than a security boundary.

## 2. Architecture Components

The primary components supporting this architecture include:

| Component | Function | Architectural Role |
|---|---|---|
| `AgentRegistration.PublicKey` | Cryptographic key binding | Device identity anchor verified on all sessions. |
| `bark.ConsentRequestPayload.RequestedPermissions` | Capability authorization | Explicit per-session capabilities (`screen_view`, `remote_control`, `clipboard`). |
| `webrtc.SignalMessage` | Envelope structure | Self-verifying signed signaling event. |
| `AgentStreamRunner.Handle` | Extensibility | Interface allowing modular transport handlers. |
| `pion/turn/v5` | Direct relay fallback | Self-hosted STUN/TURN for NAT traversal without cleartext interception. |

The foundation is present. All of these capabilities were activated and enforced across Phases 0a, 0, and 1.

## 3. Phase 0 — activate the identity already in the code

No relay network involved. This phase is worth doing whether or not the rest
is ever built.

1. **Generate a device keypair at enrolment** and populate the `public_key`
   that is already sent. The private key sits beside `agent.pem` and never
   leaves the host.
2. **Sign every control message.** The server moves from *holder of your
   secret* to *witness of your signature*. The bearer token remains as
   transport but stops being the root of trust.
3. **Extend `SignalMessage`** with `PubKey`, `Nonce`, `IssuedAt`, `ExpiresAt`
   and `Sig`, plus `Sign()`/`Verify()` and a seen-nonce cache with a window.
   This kills replay and makes each message verifiable independently of
   whatever carried it — the precondition for the carrier being untrusted.
4. **Enforce the consent that already exists.** `handleInputPayload`, clipboard
   writes, `file_complete` and `OpenReverseStream` must each consult the
   granted permissions. Today anyone who can write to that socket injects
   keyboard and mouse.
5. **Allowlist ports** in `handleReverseStream`.

Pairing changes character: the pairing code stops *issuing* trust and starts
**binding operator pubkey to device pubkey**, with the server as witness. That
out-of-band ceremony is irreducible — someone has to decide that key X may
drive machine Y, and no amount of decentralisation removes that step.

Where there is no enrolment at all — two people meeting once, a code read out
over the phone — the code is that ceremony, and it must bind the keys without
the carrier seeing it: package `pake` runs CPace over the code's secret part
and hands each end the other's ed25519 key, authenticated (barahn/remotekit#96).
That key is what `RequireSignedViewers` trusts and `ViewerConfig.AgentKey`
pins.

## 4. Phase 1 — move the data plane off the server

Without this, "decentralised" is a label on an architecture that still shows
the operator's screen to a third party.

- **Add a DataChannel** to `webrtc/peer.go` — it carries only a video track
  today — and move input, clipboard and `transfer` onto it, with
  `bark.Envelope` as the format. It is already the right envelope.
- The server then sees **SDP and ICE only**. Metadata (who, when, how long)
  stays visible; content does not.
- **Keep the JPEG fallback, but make it explicit.** As built, entering relay mode
  announces `{"type":"relay_mode","active":true,...}` to viewers and triggers
  `AgentStreamRunner.OnRelayMode`. If a deployment wishes to strictly forbid
  unencrypted JPEG frames, consumers can refuse it via `DisableRelayFallback(true)`.

## 5. Phase 2 — pluggable signalling, where a relay network fits

An interface in `remotekit`:

```go
type Signaler interface {
    Publish(ctx context.Context, msg SignalMessage) error
    Subscribe(ctx context.Context, f Filter) (<-chan SignalMessage, error)
}
```

`tunnel` becomes one implementation (WSS). A Nostr transport becomes another,
in an **optional** package (`signal/nostr`). The core must not gain a hard
dependency on it: the README commits to carrying no product opinions, and
CONTRIBUTING fixes the direction of dependencies.

### As built: `stream.SignalConn`, not a pub/sub `Signaler`

During Phase 2 implementation, the abstraction evolved from the proposed pub/sub `Signaler` interface to a simpler, more robust bidirectional byte-stream interface: `stream.SignalConn` (`ReadMessage`, `WriteMessage`, `Close`). A pub/sub bus carrying only `SignalMessage` was insufficient because session signalling carries negotiation envelopes (`session_start`, `video_ok`), notices (`relay_mode`, `consent_required`), and consumer-defined messages registered through `Handle`.

Key architectural deliveries:
- **`stream` package:** The session runner was extracted into package `stream` (`stream.Runner`), decoupled from `tunnel` and yamux (PR #83).
- **`signal/nostr` module:** Implemented as a separate standalone Go module in `signal/nostr` with its own `go.mod`, keeping `remotekit` core free of secp256k1 and Nostr dependencies.
- **Privacy & Encryption:** Built using NIP-44 v2 payload encryption, NIP-59 gift wrapping, and NIP-40 expiration.
- **Relay Hardening:** Subsequent follow-ups added automatic relay redialling with exponential backoff and grace period (#70), and NIP-42 relay authentication (#71).

Three corrections to the obvious Nostr design:

- **NIP-44**, not NIP-04, for the encrypted payload; **NIP-59 (gift wrap)** if
  relays should not learn the publisher; **NIP-40** for expiry.
- **A rotating per-device key**, never an operator's personal identity key. A
  static key published to public relays exposes the device's existence and
  activity permanently.
- **NIP-AC is a proposal, not a ratified standard.** Useful as a format
  reference, not as a stable base. Define our own kind and stay compatible
  where that is cheap.

## 6. Phase 3 — fallback without a central TURN

- **Drop the Google STUN default** (`webrtc/peer.go:44`). Every deployment
  currently contacts Google on every session. With `pion/turn/v5` already in
  the graph, running our own is trivial.
- **Ephemeral HMAC-derived TURN credentials**, scoped per session. The current
  `PeerConfig.TURNUsername`/`TURNPassword` invites a static shared secret,
  which is the classic way a TURN server becomes an open relay.
- **For native Linux clients, QUIC or WireGuard is a better data plane than
  WebRTC.** That is only reachable if transport is pluggable too;
  `agent_stream.go` hardcodes WebRTC today. The natural seam is *what to send*
  (`bark`) split from *how to send it* (`peer`).

## 7. Where a relay network does not fit

**Fleet presence.** `heartbeat` fires every 10s. Thousands of agents
publishing presence to public relays would be an absurd load and would leak a
customer's entire fleet topology. Presence stays on the operator's own control
plane.

The value of a relay network is **rendezvous when the control plane is
unreachable or untrusted** — a bootstrap and fallback path, not the primary
one.

Which yields the product conclusion. For the Barahn platform — proprietary,
fleet, enterprise — a central control plane is not a defect, it is the product:
audit, RBAC, compliance. For Chirp — on-demand support, AGPL — decentralisation
is genuinely valuable: a technician and a user who both have the app can
connect with no server at all. **The relay path belongs to Chirp first.**
`remotekit`'s job is narrower: make the signalling path swappable, and decline
to choose for either product.

## 8. Three findings that stand independently

Noted while surveying; each was an active defect at the time of writing, and all three were resolved in Phase 0a / Phase 0:

1. **`tunnel/agent_stream.go`** — when `AgentToken` is empty the client sends
   the **agent ID as the token**. *(Resolved in Phase 0a: empty tokens are rejected immediately before dialing).*
2. **`PublicKey` is never verified anywhere.** The field is decorative.
   *(Resolved in Phase 0: incoming messages verify signatures against the peer's known public key via `VerifyFrom` and enrollment records device keys).*
3. **`BARAHN_INSECURE_SKIP_VERIFY`** disables TLS verification via an
   environment variable (`tunnel/client.go`). *(Resolved in Phase 0a: `ServerKeyPin` pins the server's public key hash whenever CA verification is skipped).*

## 9. Summary

Phases 0, 1, and 2 are fully implemented and verified in the codebase:
- **Phase 0:** Cryptographic identity via Ed25519, self-authenticated `SignalMessage` with anti-replay caching, and strict consent enforcement across input, clipboard, file transfer, and screen view.
- **Phase 1:** Peer-to-peer data plane running entirely on WebRTC `bark-control` and `bark-transfer` DataChannels, with announced JPEG fallback (`relay_mode`) and opt-in strict refusal (`DisableRelayFallback`).
- **Phase 2:** Transport-agnostic signalling via `stream.SignalConn`, with an isolated, dependency-free `signal/nostr` sub-module providing NIP-59/NIP-44 gift-wrapped signalling over Nostr relays.
- **Phase 3:** Fallback and transport hardening (self-hosted STUN/TURN, ephemeral HMAC credentials, pluggable transports) is the active next step.
