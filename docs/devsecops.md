# DevSecOps in remotekit

remotekit captures screens, injects input, moves files and tunnels into hosts,
and two products with different licences ship it. A defect here is inherited by
both, and a licence problem here can make it unusable by either. The pipeline is
built around those two facts.

## The gates

| Workflow | Runs on | Gate |
| --- | --- | --- |
| `ci.yml` | push and PR to `develop`/`main`, tags, dispatch | DCO, NOTICE for vendored code, dependency licences (linux, windows, darwin), build, vet, cross-compile, lint, `go test -race` with the VP8 conformance and browser-decode tests, gosec, govulncheck |
| `secret-scan.yml` | push and PR, weekly | gitleaks over the PR's commits, or the tree and full history |
| `codeql.yml` | push and PR, weekly — **when `CODE_SCANNING` is set** | CodeQL `security-extended` for Go |
| `dependency-review.yml` | PR — **when `CODE_SCANNING` is set** | what the PR adds: advisories at `low` and above, licences outside the allow-list |
| `sbom.yml` | release tags | SPDX SBOM attached to the release |

Every workflow declares `permissions: contents: read` at the top and asks for
more only in the job that needs it. Every action is pinned to a commit SHA and
every Go tool to an exact version; Dependabot proposes updates to both, weekly,
against `develop`.

Why the overlaps are not redundant:

- **gosec vs CodeQL.** gosec matches known-bad patterns inside one function.
  CodeQL follows data from a source to a sink across packages — a remote peer's
  input reaching a file path in `transfer` or a keystroke in `input` is the
  second tool's finding.
- **govulncheck vs dependency review.** govulncheck asks whether a known
  vulnerability is *reachable* from this code, and fails the build. Dependency
  review asks what a pull request *introduces*, which catches a dependency with
  no advisory yet but a disallowed licence.
- **The licence gate vs dependency review.** Dependency review sees only what a
  pull request changes, and is off until `CODE_SCANNING` is set. The licence
  gate checks every dependency on every run, and is always on.
- **NOTICE vs the licence gate.** Modules and vendored code are different
  problems: the gate covers modules; NOTICE covers code copied into the tree,
  like the VP8 encoder.

### CodeQL and dependency review are off until `CODE_SCANNING` is set

This repository is private. On a private repository GitHub runs both only with
GitHub Advanced Security (Code Security). In barahn/chirp, in the same
organisation, both were observed failing for exactly that reason, and the
CodeQL licence permits analysing private code only under that entitlement.

Both therefore run only when the repository variable `CODE_SCANNING` is
`true`. Set it once the repository is public — where both are free — or once
Code Security is enabled. Until then every run carries a warning that the gate
is off, so its absence is visible rather than quietly green. A skipped job
counts as passing for a required check, so marking `Analyze (Go)` or
`Review dependency changes` as required before then enforces nothing.

### The dependency licence gate, and its one exception

`CONTRIBUTING.md` allows only MIT, BSD, ISC, Apache-2.0 and Zlib. The gate
enforces that with `go-licenses`, rejecting forbidden, restricted, reciprocal
and unrecognised licences, for each OS the module builds on.

**Its first run found a dependency the policy does not allow:
`github.com/hashicorp/yamux` is MPL-2.0**, and `tunnel` multiplexes over it
(`tunnel/client.go`, `server.go`, `tunnel.go`). It predates the gate and is
excepted by name, so that the gate catches every *new* copyleft dependency
without failing on day one. The exception is a decision deferred, not taken:

- **Accept MPL-2.0 formally.** It is file-level copyleft: using yamux unmodified
  generally does not reach the code around it, but anyone redistributing
  remotekit must make yamux's source available, and changes to yamux's own files
  stay MPL-2.0. Accepting it means adding MPL-2.0 to `CONTRIBUTING.md` with
  those obligations, and to dependency review's allow-list.
- **Replace yamux** with a permissively licensed multiplexer, and drop the
  exception.

Until one is chosen, do not add a second name to the exception list.

### harden-runner is in audit mode

Every job starts with `step-security/harden-runner` in `audit` mode: it records
what the job talks to on the network without blocking anything, and is
`continue-on-error` because an observer must not fail the build it observes.
Read its egress report over a few green runs, then move it to `block` with an
allowlist. Moving it blind breaks the module proxy, the linter download, and —
in `ci.yml` — the Firefox, geckodriver and libvpx downloads the tests need.

## Settings that are not in this repository

Workflows cannot grant themselves these; they are repository settings, the
half of DevSecOps a pull request cannot deliver:

1. **Branch protection on `develop` and `main`** — require pull requests,
   review from code owners (which is what makes `.github/CODEOWNERS` a gate
   rather than a suggestion), passing checks, and block force pushes.

   > **Three workflows here push directly, and branch protection would refuse
   > them.** `handoff.yml` commits the regenerated status block straight to the
   > default branch; `sync-develop.yml` pushes `main` back into `develop` after
   > a release; `release-pr.yml` opens the release pull request (that one is
   > compatible). Under a pull-request-only rule the first two fail on every
   > run. Resolve that before protecting the branches — Chirp faced the same
   > choice and replaced its bot commit with a CI check that the generated
   > block is current.

2. **Code security** — secret scanning with push protection (gitleaks in CI
   catches a secret before merge; push protection catches it before it reaches
   GitHub at all), private vulnerability reporting so `SECURITY.md` has a
   mechanism behind it, and Dependabot alerts and security updates.
3. **Actions** — workflow permissions read-only by default. The workflows that
   write already ask for it explicitly.
4. **Tag protection** for `v*.*.*`, so a release tag cannot move after an SBOM
   has been published against it.

## A note on `handoff.yml`

It runs on `pull_request_target` with a write token, which is the trigger
behind "pwn request" attacks when a workflow checks out and runs a pull
request's code. This one does not: it checks out the default branch explicitly
and runs only `scripts/gen-handoff-status.sh` from it. It does write pull
request titles — text anyone who can open a pull request controls — into a
committed file; that the script escapes them was not verified here. The
repository does not allow forks, which limits who can open one.

## When a gate fails

Fix the finding. The escape hatches, in order of preference:

1. Fix the code.
2. Suppress narrowly, in code, with a comment naming *why it is not
   exploitable* — `//nolint:` or `//#nosec` on the one line, never a file or a
   rule globally.
3. Change the gate. That is a review decision and gets its own pull request,
   not a line buried in a feature branch.

---

`SPDX-License-Identifier: Apache-2.0`
Copyright (C) 2026 Fabrintek Engenharia Digital Ltda
