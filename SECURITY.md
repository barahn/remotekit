# Security Policy

## Reporting a vulnerability

**Do not open a public issue.** Email
[security@fabrintek.com.br](mailto:security@fabrintek.com.br).

remotekit is the part of Fabrintek's remote-access products that touches the
machine: it captures the screen, injects keystrokes and mouse input, reads and
writes the clipboard, moves files, and opens a tunnel back to the host. A bug
here is rarely cosmetic. It is usually someone else's input reaching a place it
was never meant to — a path, a keystroke, a local port — on a machine whose
owner did not agree to it. Two products inherit every such bug, so treat
anything in these packages as security-sensitive until shown otherwise.

Include what you have: the affected version or commit, what an attacker gains,
and a reproduction case if you have one. A report without a proof of concept is
still worth sending.

If you would like a reply encrypted, say so in your first message and a key will
be provided.

### What to expect

| Stage | Target |
| --- | --- |
| Acknowledgement of your report | 3 working days |
| Initial assessment and severity | 10 working days |
| Fix or documented mitigation for a confirmed high/critical issue | 90 days |

These are targets, not contractual terms. If a date is going to slip you will be
told rather than left waiting.

### Disclosure

Coordinated disclosure. Please give the 90-day window above before publishing.
If a fix is not shipping in that time, you are free to disclose — the window
exists to protect users, not to bury a report. Reporters are credited in the
advisory unless they ask not to be.

A fix here usually has to reach Chirp and the Barahn platform as well. Where a
report affects them, the advisory is coordinated across all three.

## Supported versions

| Version | Supported |
| --- | --- |
| `develop` (tip) | ✅ |
| `main` (last release) | ✅ |
| `v0.1.0` | fixes land on `develop`; no backports |

## What is in scope

- **Input injection** (`input`): anything that lets injected input escape the
  session it belongs to, or arrive before the controlled user agreed to it.
- **File transfer** (`transfer`): path traversal, writes outside the intended
  directory, resumption that trusts what it should not.
- **Clipboard** (`clipboard`): content crossing to the other end without the
  session that should carry it.
- **Screen capture** (`screen`): capture that starts, continues or survives
  without a session to carry it.
- **Tunnel** (`tunnel`): enrolment, credentials, pairing-code redemption, and
  the reverse streams it opens to local ports on the host.
- **Signalling and transport** (`webrtc`, `bark`): signed-message verification,
  replay, anything that lets one peer impersonate another.
- **The build and release path**: CI configuration, action and tool pinning,
  the dependency licence gate.

Out of scope: product policy built on top of remotekit — consent rules, codes,
accounts, roles — which lives in [Chirp](https://github.com/barahn/chirp) and the
Barahn platform. Report those to the product concerned. If you are unsure
which, report here; it will be routed.

## What this pipeline enforces, and what it does not

| Gate | Question it answers | Runs |
| --- | --- | --- |
| `go vet`, golangci-lint | Is the code well-formed | every push and PR |
| `go test -race` | Do the tests pass, free of data races | every push and PR |
| gosec | Does the code match a known-bad Go pattern | every push and PR |
| govulncheck | Is a known vulnerability reachable from this code | every push and PR |
| Dependency licence gate | Is every dependency's licence on the approved list | every push and PR |
| NOTICE check | Does vendored code keep its attribution | every push and PR |
| gitleaks | Is a credential being committed | every push and PR, weekly |
| DCO sign-off | Is the origin of each commit certified | every PR |
| CodeQL (`security-extended`) | Does untrusted data reach a dangerous sink | once `CODE_SCANNING` is set |
| Dependency Review | What does this pull request *add* | once `CODE_SCANNING` is set |
| Aikido Security | Unified SAST, dependency vulnerabilities, and secrets | every push and PR (via GitHub App) |

CodeQL and dependency review need GitHub Advanced Security on a private
repository, so they are wired in but off until the `CODE_SCANNING` variable is
set; Aikido Security provides unified scanning across SAST, dependencies, and
secrets; see [docs/devsecops.md](docs/devsecops.md).

None of these is a substitute for reading the diff. They catch the classes of
mistake a reviewer reliably misses; they do not catch an injection check that
is present, correct and applied to the wrong session.

Security tooling is pinned to commit SHAs (actions) or exact versions (Go
tools), and Dependabot proposes updates weekly. A pinned tool that is never
updated reports last year's vulnerabilities.
