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

- **Branch model.** `develop` is the default and integration branch; `main`
  is the release branch. Branch from `origin/develop`, open pull requests
  against `develop`, and never push directly to either. Push with
  `git push -u origin <branch-name>`. You never open the release pull request
  yourself: `.github/workflows/release-pr.yml` opens or updates a single
  `develop` → `main` pull request on every push to `develop`, and
  `.github/workflows/sync-develop.yml` merges `main` back into `develop` once
  that release pull request is merged.
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
those workflows push. No roadmap phase has been implemented yet.

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
that rule), and the result attaches to the new tip, which is what the release
pull request shows. If you add a workflow that pushes, give it the same
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

**`develop` is at `572ff72`** — docs: refresh the handoff status block

### Open pull requests

| PR | Title | Branch | CI on head |
|---|---|---|---|
| [#14](https://github.com/barahn/remotekit/pull/14) | chore(release): merge develop → main (14 commits) — Merge pull request #13 from barahn/ci/release-pr-workflow | `develop` | no checks reported |
| [#15](https://github.com/barahn/remotekit/pull/15) | docs(handoff): describe the develop branch model and the CI gap it opened | `claude/tender-feynman-zqy5d1` | Refresh the handoff status block ✅ |
| [#16](https://github.com/barahn/remotekit/pull/16) | ci: run on develop, and on the commits workflows push there | `claude/tender-feynman-zqy5d1-ci` | Refresh the handoff status block ✅<br>Build, Test & SAST ✅ |
| [#17](https://github.com/barahn/remotekit/pull/17) | Phase 0a + signing primitive: close the three standing defects, let SignalMessage authenticate itself | `claude/sharp-wright-u0s07v` | Refresh the handoff status block ⏳ (in_progress) |

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

`docs/implementation-phases.md` is the authoritative breakdown (its status is
still *Draft / Proposed Roadmap*) and splits Phase 0 in two: **Phase 0a** is
the three defects below, which stand on their own regardless of the proposal,
and Phase 0 proper is the identity, signing and consent work above.

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

1. Clear whatever §4's generated block shows red. A failing check on an open
   pull request comes before new work, and on a docs-only branch it is usually
   the one-line DCO fix.
2. Refer to `docs/decentralised-signalling.md` and `docs/implementation-phases.md`.
   The roadmap is structured into Phase 0a (defect remediation), Phase 0b (enrolment
   identity & signing), Phase 1 (DataChannel data plane), Phase 2 (pluggable signalling),
   and Phase 3 (relays & transports).
3. The natural first slice is Phase 0a from `docs/implementation-phases.md` (port
   allowlist in `handleReverseStream`, removing insecure token fallback, and
   consent enforcement), since these harden security without breaking wire formats.
   Then Phase 0b (message signing).

Before every push: run the repo's own checks locally —
`go build ./...`, `go vet ./...`, `golangci-lint run`, and
`go test -race ./...` for the packages you touched (the full suite needs Xvfb,
`vpxdec` and Firefox, and takes tens of minutes under `-race`). Keep each
change minimal and do not widen scope on your own.

---END PROMPT---
