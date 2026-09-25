# Decentralised signalling and an untrusted control plane

**Status:** proposal, not accepted. Nothing here has been implemented.
**Implementation Roadmap:** [implementation-phases.md](implementation-phases.md)
**Scope:** `webrtc`, `tunnel`, `bark`. Policy consequences for Chirp and the
Barahn platform are noted but not decided here.

## The question this answers

Can a Nostr-style relay network replace our control plane for discovery and
signalling, the way trackers and DHT do for BitTorrent, and would that make our
remote access products decentralised and secure?

Yes to the first, with caveats. No to the second, on its own — and that second
answer is the point of this document.

## 1. The finding

**Decentralisation and security are two separate axes here, and the security
one is both more urgent and independent of any relay network.**

Today, in the centralised model, the control plane is a man-in-the-middle with
unlimited power over every session:

| What it can do | Where |
|---|---|
| Mint any agent's credential — it issues the token, so it can impersonate any agent | `tunnel/credential.go`, `tunnel/server.go` `HandlePairing` |
| See the screen — the JPEG fallback base64s full frames over the server's WebSocket, and runs whenever a viewer has not yet reported `video_ok` | `tunnel/agent_stream.go` |
| See and inject input, clipboard and file content — all of it rides the signalling WebSocket, never WebRTC | `tunnel/agent_stream.go`; there is no `DataChannel` anywhere in the module |
| Open a stream to *any* localhost port on the agent | `tunnel/client.go:270` — `127.0.0.1:%d`, no allowlist |

Swapping that server for a set of relays fixes none of these. It only changes
who the intermediary is.

> The order of work is: **make the signalling path untrusted first.** Once the
> endpoints authenticate and encrypt to each other, the path that carried the
> handshake stops being a trust decision — it can be our server, a relay, a QR
> code, or anything else. Decentralisation then becomes a transport choice
> rather than a security one.

## 2. What the module already has

Four pieces of this architecture are already in the tree. Three are inert.

| Piece | State | Role in the proposal |
|---|---|---|
| `AgentRegistration.PublicKey` (`tunnel/store.go:53`, set at `tunnel/server.go:132`) | **stored, never read** | The device identity hook. The field travels `Enroll` → `HandlePairing` → store, and nothing ever verifies a signature against it. |
| `bark.ConsentRequestPayload.RequestedPermissions` (`bark/bark.go:86`) | **defined, never enforced** | Per-session capabilities, already modelled as `["screen_view","remote_control"]`. |
| `webrtc.SignalMessage` (`webrtc/signaling.go`) | typed, transport-agnostic, **unauthenticated** | The right struct to become a signed, self-verifying event. It depends on no transport. |
| `AgentStreamRunner.Handle` (`tunnel/handler.go`) | working | The extension point that lets a different control plane be layered on without touching the core. |
| `pion/turn/v5` | already an indirect dependency | Self-hosted STUN/TURN costs no new dependency. |

The foundation is present. What is missing is closing the circuit.

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

## 4. Phase 1 — move the data plane off the server

Without this, "decentralised" is a label on an architecture that still shows
the operator's screen to a third party.

- **Add a DataChannel** to `webrtc/peer.go` — it carries only a video track
  today — and move input, clipboard and `transfer` onto it, with
  `bark.Envelope` as the format. It is already the right envelope.
- The server then sees **SDP and ICE only**. Metadata (who, when, how long)
  stays visible; content does not.
- **Keep the JPEG fallback, but make it explicit.** It is the silent default
  today, which makes "the relay need not see the content" false on that path.
  It should surface to the user as a declared degraded mode: *relay mode — the
  server can see the screen*.

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

Noted while surveying; each is a defect today, regardless of whether this
proposal is accepted.

1. **`tunnel/agent_stream.go`** — when `AgentToken` is empty the client sends
   the **agent ID as the token**. `HandleConnect` would reject it, but the
   `/signal` endpoint is implemented by the product; a server that accepts it
   turns a non-secret identifier into a credential.
2. **`PublicKey` is never verified anywhere.** The field is decorative.
3. **`BARAHN_INSECURE_SKIP_VERIFY`** disables TLS verification via an
   environment variable (`tunnel/client.go`). Under self-hosted relays this
   gets considerably more dangerous. The right answer is **pinning the
   server's key** (TOFU over the pubkey recorded at enrolment), not disabling
   verification.

## 9. Summary

A relay network is a reasonable Phase 2. Phases 0 and 1 — signed events keyed
to the pubkey already in the store, consent actually enforced, and the data
plane leaving the server over a DataChannel — deliver most of the security on
their own, and are the precondition for decentralisation to mean anything.
