# Pluggable transport seam for media and data (QUIC / WireGuard)

**Status:** Design note for Phase 3, Task 3 of [implementation-phases.md](implementation-phases.md).  
**Issue:** [#65](https://github.com/barahn/remotekit/issues/65)  
**Scope:** `stream`, `webrtc`, `bark`, downstream transports (`transport/quic`, `transport/wireguard`).

---

## 1. Context and Problem

Today, `stream.Runner` (and `stream.Viewer`) hardcodes Pion WebRTC (`*webrtc.PeerSession`) as the sole transport for media streaming (VP8 video) and user data channels (`bark-control` and `bark-transfer`).

WebRTC is indispensable when communicating with web browsers because of its ubiquity and built-in ICE/DTLS/SRTP stack. However, for native desktop environments—such as Linux-to-Linux remote administration, embedded appliances, or native client applications like Chirp:
1. **Operational overhead:** WebRTC requires complex interactive connectivity establishment (ICE) and reliance on external STUN/TURN servers to traverse stateful firewalls and symmetric NATs.
2. **Signalling complexity:** Establishing a WebRTC session requires exchanging multi-kilobyte Session Description Protocol (SDP) offers/answers and trickle ICE candidates.
3. **Protocol weight:** Packing video into RTP/SRTP and data channels into SCTP-over-DTLS-over-UDP incurs CPU and memory overhead compared to modern stream-multiplexed protocols like QUIC or kernel-level VPN tunnels like WireGuard.

Phase 2 of the roadmap made signalling transport-agnostic via `stream.SignalConn` (allowing WebSocket, Nostr relays, or in-memory transports). The media and data plane transport, however, remains fixed.

This document designs the **transport seam**: decoupling *what to send* (`bark` envelopes and VP8 frames) from *how to send it* (`webrtc.PeerSession`, QUIC, WireGuard).

---

## 2. Investigation and Core Questions

### Q1: Which `PeerSession` methods does `stream` actually depend on?

An exhaustive audit of `stream/runner.go`, `stream/dataplane.go`, `stream/displays.go`, `stream/permissions.go`, `stream/agentfiles.go`, and `stream/viewer.go` reveals that the session runner and viewer depend on only a narrow subset of `webrtc.PeerSession`:

#### A. Video Media Pipeline
- **Frame Transmission:** `WriteVideoSample(sample media.Sample) error`
- **Key-Frame Feedback (Sender):** `OnKeyFrameRequest(fn func())` — triggers an encoder intra-frame (IDR) reset when the receiver detects packet loss or begins playback.
- **Frame Reception (Viewer):** `OnVideoFrame(fn func(frame []byte, keyFrame bool))` — delivers intact VP8 video frames directly to the decoder.
- **Key-Frame Request (Viewer):** `RequestKeyFrame() error` — requests a full picture refresh from the sender.

#### B. Data Plane (Control, Clipboard, and File Transfer)
- **Message Transmission:** `SendData(label string, data []byte) error` — dispatches text or binary messages over a labeled channel (e.g., `bark-control` or `bark-transfer`).
- **Message Dispatch:** `OnDataMessage(fn func(label string, msg []byte))` — receives incoming raw envelopes.
- **Channel State:** `DataChannelOpen(label string) bool` and `OnDataChannelOpen(fn func(label string))` — validates channel availability before sending.

#### C. Session Lifecycle
- **Connection Loss Notification:** `OnConnectionLost(fn func())` — cleans up sessions when the underlying link drops.
- **Teardown:** `Close() error` — gracefully shuts down the transport.

#### D. What Stays Outside the Seam
The WebRTC-specific negotiation methods—`CreateOffer`, `CreateAnswer`, `AddICECandidate`, and `OnICECandidate`—do **not** belong in `DataTransport`. Non-WebRTC transports establish network connectivity directly (e.g. via direct UDP socket, TLS ALPN, or WireGuard handshake) using address/key metadata exchanged over `SignalConn`.

---

## 2. How Consent Stays Transport-Independent

Consent enforcement is an invariant of `remotekit`: a compromised or untrusted network transport must never be able to execute unconsented operations (such as input injection or file writing).

In `stream`:
1. `Runner` stores standing and runtime consent under `consentMu` (`r.granted`, `r.standing`, `r.effectivePermissions()`).
2. Incoming user data is routed to `dataPlane.handleChannelMessage(ctx, from, transport, label, msg)`.
3. The method parses the payload as a canonical `bark.Envelope` and invokes `d.handle(...)`, where `d.hasConsent(msgType)` is evaluated before triggering any OS injector or clipboard sync.

**Conclusion:** Consent is completely agnostic to how bytes arrive. Whether a message travels over a WebRTC DataChannel, a QUIC bidirectional stream, or a WireGuard UDP socket, it arrives as a byte slice containing a JSON `bark.Envelope`. The consent gate sits entirely above the transport seam and remains unchanged.

---

## 3. How a Non-WebRTC Transport Authenticates the Peer

In WebRTC, mutual authentication is achieved by binding:
- The sender signs the `webrtc.SignalMessage` with their Ed25519 identity key.
- The SDP payload inside the signal message contains a DTLS certificate fingerprint (`a=fingerprint:sha-256 ...`).
- Pion verifies that the TLS certificate presented during the DTLS handshake matches the signed fingerprint, preventing MITM attacks.

For non-WebRTC transports, equivalent cryptographic binding is established without public CAs:

#### 1. QUIC (TLS 1.3 Native)
- **Certificate Fingerprint Pinning:** The offering peer generates an ephemeral TLS 1.3 certificate for the QUIC connection and includes its SHA-256 fingerprint in the signed signalling offer. During the QUIC TLS handshake, the custom `tls.Config.VerifyPeerCertificate` callback validates that the certificate matches the signed fingerprint.
- **Raw Public Keys (RFC 7250 / mTLS):** Alternatively, TLS 1.3 supports negotiating raw Ed25519 public keys, binding the QUIC transport session directly to the `SigningKey` declared in `stream.Config`.

#### 2. WireGuard (Noise_IKpsk2 / Curve25519)
- WireGuard authenticates peers by static Curve25519 public keys.
- The peers exchange ephemeral WireGuard public keys and endpoint addresses inside the signed `SignalConn` handshake.
- Each peer configures the other's public key in its local WireGuard interface (`AllowedIPs` and `Endpoint`). Packets without valid cryptographic Noise authentication are discarded in kernel or user-space driver.

In both cases, peer authentication is cryptographically bound to the signed signalling identity, completely eliminating man-in-the-middle vectors without relying on third-party certificate authorities.

---

## 4. Packaging QUIC and WireGuard as Optional Modules

To keep `remotekit` core lightweight and strictly permissive, external network implementations should follow the precedent established by `signal/nostr`:
- **Core (`stream`):** Defines the abstract `DataTransport` interface and retains built-in `webrtc.PeerSession` as the default for browser interoperability.
- **Optional QUIC module (`transport/quic/`):** Contains its own `go.mod` importing `github.com/quic-go/quic-go`.
- **Optional WireGuard module (`transport/wireguard/`):** Contains its own `go.mod` importing `golang.zx2c4.com/wireguard`.

Benefits:
- `remotekit` root `go.mod` remains dependency-light, CGO-free, and purely Apache-2.0.
- Downstream projects only link the transport drivers they actually compile.
- CI verifies each transport module in dedicated matrix jobs.

---

## 5. The Interface Sketch

```go
package stream

import (
	"context"
	"time"
)

// DataTransport abstracts a bidirectional media and data plane connection
// between an agent and a viewer.
type DataTransport interface {
	// WriteVideo transmits an encoded video frame (e.g. VP8) with timestamp duration.
	WriteVideo(frame []byte, duration time.Duration) error

	// OnKeyFrameRequest registers a callback invoked when the receiver requests
	// an intra-frame (I-frame / PLI) refresh.
	OnKeyFrameRequest(fn func())

	// SendData transmits an application message on the specified channel label
	// (e.g. "bark-control", "bark-transfer").
	SendData(label string, data []byte) error

	// OnDataMessage registers a handler for incoming messages across all channels.
	OnDataMessage(fn func(label string, msg []byte))

	// DataChannelOpen reports whether the given channel label is ready for transmission.
	DataChannelOpen(label string) bool

	// OnDataChannelOpen registers a callback notified when a labeled channel opens.
	OnDataChannelOpen(fn func(label string))

	// OnConnectionLost registers a callback notified when the underlying transport drops.
	OnConnectionLost(fn func())

	// Close terminates the transport session.
	Close() error
}

// VideoReceiver is an optional interface implemented by transports capable of
// receiving video frames (such as Go-based viewers).
type VideoReceiver interface {
	// OnVideoFrame registers a callback receiving raw VP8 video frames.
	OnVideoFrame(fn func(frame []byte, keyFrame bool))

	// RequestKeyFrame signals the remote sender to emit a key-frame.
	RequestKeyFrame() error
}
```

### WebRTC Adapter
Existing `*webrtc.PeerSession` trivially implements `DataTransport` via a lightweight adapter wrapping `WriteVideoSample`, `OnKeyFrameRequest`, `SendData`, `OnDataMessage`, and `DataChannelOpen`.

---

## 6. Downstream Impact and Roadmap Integration

1. **Chirp (AGPL-3.0 - On-Demand Linux Support):**
   - Directly benefits from QUIC/WireGuard transport: eliminates STUN/TURN latency and packet overhead for Linux technician-to-client sessions.
2. **Barahn (Enterprise Remote Access):**
   - Retains WebRTC for browser viewers (HTML5/WebAssembly console) while allowing native agents and relays to use QUIC for headless background streaming.
3. **Transition Path:**
   - Step 1: Formalize `DataTransport` interface in `stream/transport.go`.
   - Step 2: Wrap `webrtc.PeerSession` behind the interface in `stream/runner.go` with zero regression.
   - Step 3: Implement `transport/quic` in a separate module under Phase 3.
