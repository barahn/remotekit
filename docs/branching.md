# Branches, protection and merge flow

## Model

remotekit follows **Trunk-Based Development**.

`main` is the trunk: every working branch starts from it, and every pull
request targets it.

```
feature/*  fix/*  deps/*  docs/*  ──▶  main  ──(tag)──▶  vX.Y.Z
```

A release is a tagged point on `main` — an annotated `vX.Y.Z`, with
`signal/nostr/vX.Y.Z` for the module of its own ([release.md](release.md)).
There is no release branch, no release pull request, and nothing merged back
after a release.

Branch names: `feature/<topic>`, `fix/<topic>`, `deps/<topic>`,
`docs/<topic>`. Branches under `claude/` are agent-authored.

There is no `hotfix/*` branch. A released version gets no backports
([SECURITY.md](../SECURITY.md#supported-versions)), so an urgent fix is a
`fix/*` pull request like any other, shipped as the next patch tag.

## What the protection guarantees

`main` accepts neither a direct push, a force-push nor deletion. The rules
live in a ruleset on GitHub, applied by the maintainer; this table is what it
currently enforces.

| Rule | `main` |
| --- | --- |
| Direct push | blocked |
| Force-push / deletion | blocked |
| Pull request required | yes |
| Approvals | 0 — see below |
| Approval dismissed on each push | yes |
| Code owner review | required |
| Branch up to date with base before merge | yes |
| Merge method | merge commit — see below |
| Signed commits | required — see below |

Required checks: `Build, Test & SAST`, `DCO Sign-off`, `signal/nostr module`,
`gitleaks` and `Analyze (Go)` (CodeQL). CodeQL and dependency review run only
while the `CODE_SCANNING` variable is set — see [devsecops.md](devsecops.md).
Where it is not set they are skipped, and a skipped required check counts as
passing, so they enforce nothing there.

`bypass_actors` is empty on every ruleset, on purpose: the rules bind
administrators too. A workflow that needs to write to `main` — only
`handoff.yml` does — commits through the GitHub API, which signs what it
creates, rather than asking for a bypass.

Release tags are protected separately: `refs/tags/v*.*.*` cannot be deleted or
moved once it exists, by anyone. A published version is in the Go module proxy
and checksum database for good, so a tag that could move would break every
build that fetched the old one.

### The merge method, and signed commits

`main` requires signed commits today, and the only allowed merge method is a
merge commit, which GitHub creates and signs.

The agreed direction is **rebase with linear history, and
`required_signatures` off**. Rebase keeps `git log` readable and bisectable and
lands each commit with its verified DCO `Signed-off-by` trailer intact; squash
is rejected because GitHub's squash commit drops the individual DCO
attestations. The two cannot be combined with required signatures: GitHub
cannot sign the commits of a rebase merge — it rewrites each one and has no key
of the author's — so with rebase as the only merge method every commit would
land unsigned, which is exactly what `required_signatures` refuses. A DCO
trailer is an attestation, not a cryptographic signature, and the trailer is
what the four repositories check.

That ruleset change is the maintainer's to apply; until he does, the table
above is the live state.

## The approval policy, as decided

**One maintainer cannot approve their own pull requests**, so requiring
approvals on a single-maintainer repository would block every merge unless
bypass is granted. GitHub's bypass lifts *every* rule in the ruleset for that
actor, status checks included, which is unacceptable.

The maintainer's decision:
- **`main`: no approval required, and no bypass.** A pull request is still the
  only way in, and every other rule — the required checks, up to date with
  base, code owner review — binds the maintainer like anyone else.
- When a second maintainer joins with write access, raise
  `required_approving_review_count` to 1.

Nothing about this weakens the standing rule that nothing reaches a release
before it is designed, demonstrated, tested and approved.

### Before trunk-based development

Until 2026-10-28 there were two long-lived branches: `develop`, the default
and integration branch, and `main`, the release branch, reached only by a
merge commit from `develop`. That model cost a workflow to keep the release
pull request open (`release-pr.yml`), a second one to push `main` back into
`develop` after every release (`sync-develop.yml`), a CI job asserting that a
pull request to `main` was headed by `develop` (`Release source`), and a
`develop` that reported itself behind `main` after each release.

It also cost a bypass. `sync-develop.yml` had to move `develop`'s ref, and the
only way to allow that under the `develop` ruleset was to let the GitHub
Actions actor bypass it — which lifted the whole ruleset for that actor, not
just the push rule. Retiring `develop` removes the reason for the bypass.

`develop` was emptied into `main` first (#162), so nothing was left behind;
`origin/main..origin/develop` was empty when the machinery came out.
`main`'s history up to that merge keeps those merge commits.

## Working flow

```sh
git switch main && git pull
git switch -c fix/transfer-path-check

# ... changes ...
git commit -s          # -s is required: CI checks the DCO trailer

git push -u origin fix/transfer-path-check
# open the pull request against main:
gh pr create --base main
```

To release, follow [release.md](release.md): it is approved first, then tagged
on `main`.

The security posture behind all of this — which gates run, why they overlap,
and the repository settings that cannot live in a file — is in
[devsecops.md](devsecops.md).

`SPDX-License-Identifier: Apache-2.0`
Copyright (C) 2026 Fabrintek Engenharia Digital Ltda
