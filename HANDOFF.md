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
| `tunnel` | WSS + Yamux reverse tunnel, agent enrolment, multiplexed streams |
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

- Develop on branch **`claude/tender-feynman-zqy5d1`**; create it from
  `origin/main` if needed. Never push to `main`. Push with
  `git push -u origin claude/tender-feynman-zqy5d1`.
- **Every commit needs a DCO `Signed-off-by` trailer** (`git commit -s`). CI
  enforces it on every non-merge commit in a PR and this has already broken one
  PR — see §4. `CONTRIBUTING.md` also carries a CLA; sign-off is not the CLA.
- CI additionally enforces that every directory containing a
  `LICENSE.upstream` is named in `NOTICE` (Apache-2.0 §4(d)). If you vendor
  anything, update `NOTICE` in the same commit.
- Do not open a pull request unless explicitly asked.

## 3. State of the tree

`main` is green at commit `e5b3d72` ("Merge pull request #8 from
barahn/chore/pion-transport-v5"). `claude/tender-feynman-zqy5d1` currently sits
at the same commit — no unpushed work, no local diff.

What has landed so far: the module stood up independently; the CI pipeline with
the VP8 conformance oracle; 57 lint findings cleared; `golang.org/x/net` bumped
to v0.56.0 for GO-2026-5942; the VP8 encoder reconciled with the copy in
`barahn/barahn`; a real agent credential issued instead of a derived one;
multi-monitor capture and injection carried forward; CONTRIBUTING + CLA + DCO
and NOTICE enforcement; `pion/transport/v5` with the browser tests restored;
the browser decode test now *runs* rather than skipping.

### The CI pipeline (`.github/workflows/ci.yml`, single job, 20 min cap)

DCO → NOTICE check → `go build`/`go vet` → cross-compile for
windows/amd64 and darwin/amd64 → golangci-lint v2.13.2 → install Xvfb,
`vpx-tools`, Firefox (Mozilla tarball, not apt) and geckodriver v0.36.0 →
`xvfb-run -a go test -race -timeout 30m -coverprofile=coverage.out ./...` with
`BARAHN_BROWSER_TEST=1` and `VP8_CONFORMANCE_REQUIRED=1` → gosec v2.22.1
(excluding `screen/codec/vp8`, which has 53 deliberate int→int16 narrowings the
spec bounds by construction) → govulncheck v1.7.0.

Two environment variables exist specifically to turn *skips* into *failures*:
`BARAHN_BROWSER_TEST` for `TestFirefoxDecodesTheStream` and
`VP8_CONFORMANCE_REQUIRED` for the libvpx oracle. Never weaken either, and
never skip, disable or quarantine a test to get green.

## 4. Open pull requests — deal with these first

**PR #10 — `docs(vp8): record that a browser decodes the stream`**
(branch `claude/elegant-bell-ea29qg`, https://github.com/barahn/remotekit/pull/10)
README-only change replacing the "not yet validated in a browser" caveat with
the fact that headless Firefox decodes the stream over real WebRTC via
`TestFirefoxDecodesTheStream`, while noting that what a browser *paints* is
still nobody's measurement. **CI is red for exactly one reason: commit
`d7abde6` has no `Signed-off-by` trailer.** Fix: re-sign that commit
(`git commit --amend -s`, or rebase with sign-off) and force-push that branch —
it is a Claude-authored branch, so rewriting it is fine — then confirm CI goes
green. Nothing else is wrong with it.

**PR #11 — `docs: propose an untrusted control plane, and a swappable
signalling path`** (branch `claude/sharp-wright-u0s07v`,
https://github.com/barahn/remotekit/pull/11) — CI green. Adds
`docs/decentralised-signalling.md`, a design proposal (explicitly *not
accepted*). It is the roadmap, summarised in §5.

## 5. The roadmap, and the security findings inside it

PR #11's core argument: decentralisation and security are separate axes, and
today the control plane is a man-in-the-middle with unlimited power — it mints
agent credentials (so it can impersonate any agent), the JPEG fallback
base64s full frames over the server's WebSocket whenever a viewer has not yet
reported `video_ok`, and input, clipboard and file content all ride the
signalling WebSocket because **there is no DataChannel anywhere in the
module**. Swapping the server for relays fixes none of that.

Ordered work, as proposed:

- **Phase 0 — activate the identity already in the code.** Generate a device
  keypair at enrolment and actually populate/verify `AgentRegistration.PublicKey`
  (`tunnel/store.go:53`, set at `tunnel/server.go:132`, *stored and never
  read*). Sign every control message; extend `webrtc.SignalMessage` with
  `PubKey`, `Nonce`, `IssuedAt`, `ExpiresAt`, `Sig` plus `Sign()`/`Verify()`
  and a seen-nonce cache. Enforce `bark.ConsentRequestPayload.RequestedPermissions`
  (`bark/bark.go:86`, *defined and never enforced*) in `handleInputPayload`,
  clipboard writes, `file_complete` and `OpenReverseStream`. Allowlist ports in
  `handleReverseStream` — `tunnel/client.go:270` dials `127.0.0.1:%d` with no
  allowlist at all. Worth doing whether or not the rest is ever built.
- **Phase 1 — move the data plane off the server.** Add a DataChannel to
  `webrtc/peer.go` (video track only today) and carry input, clipboard and
  `transfer` over it with `bark.Envelope` as the format. Keep the JPEG fallback
  but surface it as a declared degraded mode ("relay mode — the server can see
  the screen") instead of a silent default.
- **Phase 2 — pluggable signalling.** A `Signaler` interface
  (`Publish`/`Subscribe`) with `tunnel` as the WSS implementation and an
  *optional* `signal/nostr` as another. If Nostr: NIP-44 (not NIP-04), NIP-59
  gift wrap if relays must not learn the publisher, NIP-40 for expiry, and a
  rotating per-device key — never an operator's personal identity key. NIP-AC
  is a proposal, not a ratified standard.
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

Three defects that stand on their own regardless of the proposal:

1. `tunnel/agent_stream.go` — when `AgentToken` is empty the client sends the
   **agent ID as the token**. `HandleConnect` rejects it, but `/signal` is
   implemented by the product; a lenient server turns a non-secret identifier
   into a credential.
2. `PublicKey` is never verified anywhere — the field is decorative.
3. `BARAHN_INSECURE_SKIP_VERIFY` disables TLS verification via an environment
   variable (`tunnel/client.go`). The right answer is pinning the server key
   (TOFU over the pubkey recorded at enrolment), not disabling verification.

Also open, unrelated to the above: macOS is stubbed out —
`input/input_darwin.go` and `screen/capture_darwin.go` both return
"not yet implemented" and carry `TODO` notes pointing at CGEvent and
CGDisplayStream/ScreenCaptureKit respectively. Doing that without CGo is the
hard part; discuss before starting.

## 6. What to do now

1. Fix PR #10's missing sign-off and get it green. Small, unambiguous, unblocks
   a correct README.
2. Ask the repository owner whether PR #11 is accepted as the roadmap before
   implementing any phase of it. The document says "proposal, not accepted" and
   Phase 0 changes the trust model — that is not yours to decide unilaterally.
3. Once (2) is answered, the natural first slice is Phase 0 item 5 (port
   allowlist in `handleReverseStream`) plus the consent enforcement, since
   neither changes any wire format. Then the signing work, which does.

Before every push: run the repo's own checks locally —
`go build ./...`, `go vet ./...`, `golangci-lint run`, and
`go test -race ./...` for the packages you touched (the full suite needs Xvfb,
`vpxdec` and Firefox, and takes tens of minutes under `-race`). Keep each
change minimal and do not widen scope on your own.

---END PROMPT---
