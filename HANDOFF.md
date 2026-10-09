# remotekit — prompt de continuidade para outro agente

Copie o bloco inteiro abaixo (da linha `---BEGIN PROMPT---` até
`---END PROMPT---`) como primeira mensagem para o próximo agente. Ele é
autossuficiente: não depende de nenhuma conversa anterior.

---BEGIN PROMPT---

You are taking over an in-progress Go project. Read this whole brief before
touching anything; it is the only context you get.

## 1. The repository

`github.com/barahn/remotekit` — Apache-2.0, Go 1.26.5, no CGo, module root at
the repository root. It is the shared remote-desktop core behind two products
with incompatible licences: **Chirp** (AGPL-3.0, on-demand remote support) and
**Barahn** (proprietary fleet management). Neither can consume the other's code,
so the mechanism both need lives here under a permissive licence.

Packages (each usable on its own):

| Package | Role |
|---|---|
| `screen` | Capture (X11, Wayland, Windows, macOS), damage detection, YUV conversion, VP8 encoding, multi-monitor |
| `screen/codec/vp8` | Pure-Go VP8 encoder, vendored from `mendsec/vp8` (fork of `opd-ai/vp8`) |
| `screen/codec/ivf` | IVF container writer |
| `screen/codec/vp8check` | Conformance oracle helper (shells out to libvpx `vpxdec`) |
| `input` | Keyboard/mouse injection, multi-monitor aware |
| `clipboard` | Cross-platform read/write plus change watcher |
| `transfer` | Chunked file transfer with resume |
| `webrtc` | pion wrapper: peer session, video track, signalling helpers |
| `stream` | Session runner over any `SignalConn`: negotiation, screen, data plane, consent, signed viewers; imports neither `tunnel` nor yamux |
| `tunnel` | WSS + Yamux reverse tunnel, agent enrolment, multiplexed streams; `AgentStreamRunner` wraps a `stream.Runner` |
| `bark` | Shared wire envelope |
| `heartbeat` | Keepalive with server-side staleness monitoring |
| `tray`, `service`, `osinfo` | Tray icon, OS service install (systemd/launchd/SCM), host details |

### Non-negotiable design constraints

- **No product opinions in this module.** No user, tenant, role, or session
  policy anywhere. Nothing shells out to run commands on the caller's behalf.
  Features layer on through extension points such as
  `tunnel.AgentStreamRunner.Handle`.
- **Dependency direction is fixed:** products depend on `remotekit`, never the
  reverse. An optional transport goes in its own subpackage; the core must not
  gain a hard dependency on it.
- **No CGo.** libvpx is only ever a *test-time subprocess*, never linked.

## 2. Ground rules you must follow

- **Branch model.** `develop` is the default and integration branch; `main`
  is the release branch. Branch from `origin/develop`, open pull requests
  against `develop`, and never push directly to either. Push with
  `git push -u origin <branch-name>`. You never open the release pull request
  yourself: `.github/workflows/release-pr.yml` opens or updates a single
  release pull request, `develop` into `main`, on every push to `develop`.
  `main` takes pull requests from `develop` only (the required `Release
  source` check refuses any other head) and only as a merge commit, so
  `develop`'s commits become part of `main`. Once it merges,
  `.github/workflows/sync-develop.yml` fast-forwards `develop` to `main`, or
  merges `main` into it if `develop` moved meanwhile; `main` is then behind
  `develop` by exactly what is unreleased. There are no other long-lived
  branches. `scripts/release-pr.sh` and `scripts/sync-develop.sh` have the
  details. Both `develop` and `main` require signed commits. `develop` was
  rebuilt on `main` on 2026-10-04 to get there: its history until then
  carried 39 unsigned handoff refreshes, which `main` cannot take, and was
  not kept.
- **Every commit needs a DCO `Signed-off-by` trailer** (`git commit -s`). CI
  enforces it on every non-merge commit in a pull request, and a missing
  trailer has already turned one pull request red. `CONTRIBUTING.md` also carries a CLA; sign-off is not the CLA.
- CI additionally enforces that every directory containing a
  `LICENSE.upstream` is named in `NOTICE` (Apache-2.0 §4(d)). If you vendor
  anything, update `NOTICE` in the same commit.
- Do not open a pull request unless explicitly asked.

## 3. State of the tree

The live figures — the default branch's tip, the open pull requests and their CI verdict —
are regenerated on every push to the default branch (`develop`) by `.github/workflows/handoff.yml`, and
sit in the generated block in §4. Read that block for current state; the prose
around it is the durable part.

What has landed so far: the module stood up independently; the CI pipeline with
the VP8 conformance oracle; 57 lint findings cleared; `golang.org/x/net` bumped
to v0.56.0 for GO-2026-5942; the VP8 encoder reconciled with the copy in
`barahn/barahn`; a real agent credential issued instead of a derived one;
multi-monitor capture and injection carried forward; CONTRIBUTING + CLA + DCO
and NOTICE enforcement; `pion/transport/v5` with the browser tests restored;
the browser decode test now *runs* rather than skipping, and the README says
so; the signalling proposal (`docs/decentralised-signalling.md`) and its
engineering breakdown (`docs/implementation-phases.md`); this brief with its
generated status block; the `develop`/`main` branch model with the
release-PR and sync workflows; and CI extended to `develop` and to the commits
those workflows push; Phase 0a plus the Phase 0 signing primitive
(barahn/remotekit#17), described in §5; Aikido Security integration
via the GitHub App with repository configuration in `.aikido`; and
automated stale branch cleanup via `.github/workflows/cleanup-branches.yml`,
`scripts/cleanup-branches.sh`, and GitHub repository `delete_branch_on_merge`.
Nothing past that has been implemented.

### The CI pipeline (`.github/workflows/ci.yml`, single job, 20 min cap)

DCO → NOTICE check → `go build`/`go vet` → cross-compile for
windows/amd64 and darwin/amd64 → golangci-lint v2.13.2 → install Xvfb,
`vpx-tools`, Firefox (Mozilla tarball, not apt) and geckodriver v0.36.0 →
`xvfb-run -a go test -race -timeout 30m -coverprofile=coverage.out ./...` with
`BARAHN_BROWSER_TEST=1` and `VP8_CONFORMANCE_REQUIRED=1` → gosec v2.22.1
(excluding `screen/codec/vp8`, which has 53 deliberate int→int16 narrowings the
spec bounds by construction) → govulncheck v1.7.0.

**Which commits CI covers.** `ci.yml` runs on pushes and pull requests for
both `main` and `develop`, and on `v*.*.*` tags. Commits that a workflow pushes
need extra care: a push made with `GITHUB_TOKEN` starts no workflow, and a
pull request run it causes waits in *action_required* for a human to approve.
The handoff refresh and the `sync-develop` merge both push that way, so each
dispatches `ci.yml` on the branch afterwards (`workflow_dispatch` is exempt from
that rule), and the result attaches to the new tip. The release pull request
is opened the same way, so its `pull_request` runs -- `Release source`, DCO,
CodeQL, secret scanning and dependency review -- wait in *action_required*
until a maintainer approves them. If you add a workflow that pushes, give it the same
dispatch step, or its commits will go unchecked. Whatever the cause, an absent
check is never a pass.

Two environment variables exist specifically to turn *skips* into *failures*:
`BARAHN_BROWSER_TEST` for `TestFirefoxDecodesTheStream` and
`VP8_CONFORMANCE_REQUIRED` for the libvpx oracle. Never weaken either, and
never skip, disable or quarantine a test to get green.

## 4. Current state — generated, do not hand-edit

Everything between the two markers below is written by
`scripts/gen-handoff-status.sh`, run by `.github/workflows/handoff.yml` on every
push to the default branch (`develop`). Edit the script, not the block.

Reading it: an open pull request with a red check is work before anything else.
Two failures recur in this repository and are worth ruling out first — a commit
missing its `Signed-off-by` trailer (the DCO step, and the usual cause on a
docs-only branch), and a vendored directory absent from `NOTICE`. Both are
reported by name in the failing job's log. A row reading "no checks
reported" is not green — see §3 for which commits CI covers. Branches under `claude/` are
agent-authored, so re-signing a commit and force-pushing is fine; never rewrite
a branch someone else owns.

<!-- BEGIN GENERATED: handoff status -->

_Generated from `barahn/remotekit` by `scripts/gen-handoff-status.sh`._

**`develop` is at `9b9dede`** — docs: refresh the handoff status block

### Open pull requests

| PR | Title | Branch | CI on head |
|---|---|---|---|
| [#86](https://github.com/barahn/remotekit/pull/86) | chore(release): merge develop → main (12 commits) — Merge pull request #84 from barahn/claude/stream-runner | `develop` | no checks reported |

<!-- END GENERATED: handoff status -->

## 5. The roadmap, and the security findings inside it

The argument in `docs/decentralised-signalling.md` (detailed in the companion
implementation roadmap `docs/implementation-phases.md`): decentralisation and security are separate axes, and
today the control plane is a man-in-the-middle with unlimited power — it mints
agent credentials (so it can impersonate any agent), the JPEG fallback
base64s full frames over the server's WebSocket whenever a viewer has not yet
reported `video_ok`, and input, clipboard and file content all ride the
signalling WebSocket because **there is no DataChannel anywhere in the
module**. Swapping the server for relays fixes none of that.

Ordered work, as proposed:

- **Phase 0 — activate the identity already in the code.** Generate a device
  keypair at enrolment and actually populate/verify `AgentRegistration.PublicKey`
  (`tunnel/store.go`, set in `tunnel/server.go`, *stored and never read*).
  Sign every control message and verify it on receipt. Enforce
  `bark.ConsentRequestPayload.RequestedPermissions` (`bark/bark.go`, *defined
  and never enforced*) in `handleInputPayload`, clipboard writes,
  `file_complete` and `OpenReverseStream`. **Already done:** the signing
  primitive, the device keypair at enrolment, signing of the agent's outgoing
  answers and candidates, consent enforcement for input, clipboard and
  file transfer — see "What #17 landed" and "Consent enforcement" below — the
  server verifying `PublicKey` (a proof of possession on every connection and
  enrolment bound to the key, #18), and the agent verifying viewers' offers
  and candidates on receipt (`AgentStreamRunner.RequireSignedViewers`,
  opt-in; a consumer supplies which viewer keys it trusts), and consent on
  reverse streams (`TunnelClient.ConsentReverseStream`, opt-in, asked after
  the port allowlist), and consent on the screen itself
  (`AgentStreamRunner.RequireScreenViewConsent`, opt-in).
  Input, clipboard and file messages are not signed themselves: Phase 1
  moves them onto a data channel inside the DTLS session the signed offer
  and answer set up.
- **Phase 1 — move the data plane off the server.** The viewer opens
  `bark-control` and `bark-transfer` data channels, `webrtc.PeerSession`
  accepts them (`webrtc/datachannel.go`), and input, clipboard and
  `transfer` travel over them as `bark.Envelope`s (`tunnel/dataplane.go`).
  The socket path stays until the products' viewers open the channels;
  `AgentStreamRunner.RequireDataChannel` is the opt-in that refuses it.
  The JPEG fallback is announced whenever it runs (`relay_mode` to the
  viewer, `OnRelayMode` to the consumer) and refused with the opt-in
  `DisableRelayFallback`.
- **Phase 2 — pluggable signalling.** The seam is `tunnel.SignalConn`
  (read a message, write a message, close) with
  `AgentStreamRunner.Serve(ctx, conn)` to run a session over it; `Start` is
  the WebSocket implementation. It is narrower than the `Publish`/`Subscribe`
  `Signaler` first proposed, because the socket carries far more than
  `SignalMessage` — see "As built" under Phase 2 in
  `docs/implementation-phases.md`. The optional relay transport is
  `signal/nostr`, a module of its own (own `go.mod`, own CI job) so the core
  never imports it: `nostr.Listen` (agent) and `nostr.Dial` (viewer) return a
  `*nostr.Conn` that is a `SignalConn`. Messages are NIP-59 gift wraps (seal
  signed by the sender, wrap by a one-time key) encrypted with NIP-44 and
  expiring by NIP-40; the rumor kind is our own, 21059, since NIP-AC is a
  proposal, not a standard. Keys are generated per session by default — never
  an operator's personal identity key. The Nostr key addresses, it does not
  authenticate: a Listen conn labels each message with its sender as
  `viewer_id`, and `RequireSignedViewers` is still what decides who a viewer
  is. A message must fit about 27–40 KB after wrapping, so a deployment over
  relays wants `DisableRelayFallback` and `RequireDataChannel` on. How the
  viewer learns the agent's session key and relays (support code, QR, link) is
  the product's rendezvous to design.
- **Phase 3 — fallback without a central TURN.** Drop the Google STUN default
  (`webrtc/peer.go:44`); every deployment currently contacts Google on every
  session, and `pion/turn/v5` is already an indirect dependency. Use ephemeral
  HMAC-derived per-session TURN credentials rather than the static shared
  secret the current `PeerConfig.TURNUsername`/`TURNPassword` invites. For
  native Linux clients QUIC or WireGuard beats WebRTC, which needs the
  *transport* to be pluggable too — `agent_stream.go` hardcodes WebRTC; the
  seam is *what to send* (`bark`) versus *how to send it* (`peer`).
- **Explicitly out of scope for relays:** fleet presence. `heartbeat` fires
  every 10s; publishing that to public relays would leak a customer's whole
  fleet topology. The relay path's value is rendezvous when the control plane
  is unreachable or untrusted, and it belongs to Chirp first, not Barahn.

`docs/implementation-phases.md` is the authoritative breakdown (its status is
still *Draft / Proposed Roadmap*) and splits Phase 0 in two: **Phase 0a**, the
three standalone defects, and Phase 0 proper, the identity, signing and consent
work above.

### What #17 landed (Phase 0a, plus the signing primitive)

- **No agent ID as token.** `AgentStreamRunner.Start` refuses to dial without
  an `AgentToken` and returns for good, logging once; it reports no error, so
  callers check the token themselves (its doc comment says so).
- **Reverse-stream port allowlist — a behaviour change for consumers.**
  `TunnelClient.AllowedReversePorts` is **deny-by-default**: an empty list
  refuses every reverse stream with `ErrPortNotPermitted`. Any consumer that
  uses reverse streams (the SSH tunnel) must set it, typically to `[]int{22}`.
  Chirp and the platform both need that one-line change when they take this
  version.
- **Server key pin.** `Enroll` records the SHA-256 of the server's public key
  as `AgentCredentials.ServerKeyPin`. The pin is enforced **only when CA
  verification is off** (`BARAHN_INSECURE_SKIP_VERIFY`), where it turns "trust
  anything" into "trust this key". With CA verification on, the CA decides and
  the pin is not consulted — deliberately: enforcing it there would turn an
  ACME renewal onto a new key, or a load balancer with differing backend keys,
  into a fleet-wide outage recoverable only by re-enrolling every agent. Do
  not "tighten" this without solving key rotation first.
- **Signing primitive.** `webrtc/signed.go`: `SignalMessage`
  gains `PubKey`, `Nonce`, `IssuedAt`, `ExpiresAt`, `Sig` (Ed25519), with
  `Sign`, `Verify`, `VerifyFrom` and a `NonceCache`. Gate actions on
  `VerifyFrom` with the peer's known key, never on `Verify` alone, and call
  `NonceCache.CheckAndRecord` only after verification succeeds. Signed bytes
  are a length-prefixed layout behind the `remotekit/signal/v1` domain tag, not
  JSON; bump the tag if that layout ever changes. Unsigned messages encode
  exactly as before. The agent signs its outgoing answers and candidates
  (`encodeSignal`); nothing yet calls `VerifyFrom` or `NonceCache` on a message
  it receives.

### Consent enforcement — a behaviour change for consumers

`AgentStreamRunner` keeps the permissions the person at the machine has
granted (`tunnel/consent.go`) and is **deny-by-default**: `remote_control`
gates `handleInputPayload`, `clipboard` gates clipboard writes and the
host-to-technician clipboard watcher, `file_transfer` gates `file_start`,
`file_chunk` and `file_complete`. A consumer must show its own consent prompt
(a `bark.ConsentRequestPayload` arrives through `Handle`) and call
`Grant(...)` with what the user accepted; without it input, clipboard and
files silently do nothing, with one log line per refused permission. Chirp and
the platform both need that change when they take this version. Nothing on the
signalling socket can grant — the server is not who consents — and a `close`
message revokes everything `Grant` gave. Where nobody is at the machine to ask,
as on an unattended fleet host, the consumer names what its own policy allows
with `SetStandingPermissions`; those hold across sessions, and neither `close`
nor `Revoke` withdraws them.

`screen_view` is enforced only when a consumer calls
`RequireScreenViewConsent(true)`, deliberately opt-in: enforcing it for
everyone would stop the screen for every consumer that does not `Grant` it.
With it on, an `offer` or `session_start` is refused until `screen_view` is
granted, the viewer is sent `{"type":"consent_required","permission":"screen_view"}`
so it can say why, and frames stop as soon as the permission is revoked.
Signature checks (`RequireSignedViewers`) run first, so an unsigned message
learns nothing about consent.

Reverse streams (SSH through the tunnel) belong to `TunnelClient`, not the
session, so they follow a separate, opt-in rule. `AllowedReversePorts` is the
administrator's policy and always applies. `ConsentReverseStream`, when set, is
asked after the allowlist passes and before each stream is opened; leave it nil
on unattended fleet hosts, where nobody is there to ask. To tie it to the
session, set it to `runner.Granted(tunnel.PermissionReverseStream)`, so the
stream is refused until the user grants it and again once the session revokes.

Also open, unrelated to the above: macOS is stubbed out —
`input/input_darwin.go` and `screen/capture_darwin.go` both return
"not yet implemented" and carry `TODO` notes pointing at CGEvent and
CGDisplayStream/ScreenCaptureKit respectively. Doing that without CGo is the
hard part; discuss before starting.

## 6. What to do now

1. Clear whatever §4's generated block shows red. A failing check on an open
   pull request comes before new work, and on a docs-only branch it is usually
   the one-line DCO fix.
2. Refer to `docs/decentralised-signalling.md` and `docs/implementation-phases.md`.
   The roadmap is structured into Phase 0a (defect remediation), Phase 0b (enrolment
   identity & signing), Phase 1 (DataChannel data plane), Phase 2 (pluggable signalling),
   and Phase 3 (relays & transports).
3. Phases 0 and 1 are done in this module: device keys, the server
   verifying them, signing in both directions, consent for input, clipboard,
   file transfer, reverse streams and the screen, and the data plane on a
   WebRTC data channel with the JPEG fallback announced. Five of those
   protections are opt-in and only protect a deployment once a consumer turns
   them on — `RequireSignedViewers`, `ConsentReverseStream`,
   `RequireScreenViewConsent`, `RequireDataChannel` and
   `DisableRelayFallback`; that wiring lives in the products, which also have
   to open the `bark-control` and `bark-transfer` channels from their viewers.
   Phase 2 is done: `SignalConn` and `Serve` let a session run over any
   transport, and `signal/nostr` is the optional relay transport, in its own
   module. Since barahn/remotekit#83 the session runner is package `stream`
   (`stream.New(stream.Config{ID, SigningKey})`), so a consumer with no
   enrolment -- Chirp -- serves a session without importing `tunnel` or
   yamux; `tunnel.AgentStreamRunner` wraps it unchanged for Barahn. Wiring it into a product (the rendezvous that hands a viewer the
   agent's session key and relays, and turning on the opt-ins above) is the
   products' work. The next phase in this module is Phase 3.

Before every push: run the repo's own checks locally —
`go build ./...`, `go vet ./...`, `golangci-lint run`, and
`go test -race ./...` for the packages you touched (the full suite needs Xvfb,
`vpxdec` and Firefox, and takes tens of minutes under `-race`). Keep each
change minimal and do not widen scope on your own.

---END PROMPT---
