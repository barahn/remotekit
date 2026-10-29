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

`main` accepts neither a direct push, a force-push nor deletion. The rules live
in a ruleset on GitHub, applied by the maintainer, and are versioned here as
`.github/rulesets/main-protected.json` and `tags-protected.json`. Those files
are the reference: **anything configured on GitHub and not in them is drift**,
and the table below is a readable summary of the same thing.

They were generated from the live rulesets rather than written as a wish, so
they record what is enforced today — including the two rules the agreed
direction would remove, `required_signatures` and the merge-commit-only method.
A file that recorded the intention instead would read as if the decision had
been applied. Re-generate rather than edit by hand:

```
gh api repos/barahn/remotekit/rulesets/23966128 > /tmp/live.json   # main
gh api repos/barahn/remotekit/rulesets/25610386 > /tmp/live.json   # tags
```

keeping `name`, `target`, `enforcement`, `conditions`, `bypass_actors` and
`rules`, and dropping the server's own `id`, `source` and timestamps. To apply
a change the other way: `gh api -X PUT repos/barahn/remotekit/rulesets/<id>
--input <file>`.

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
| Review threads resolved before merge | not required |

Required checks: `Design hold`, `Build, Test & SAST`, `DCO Sign-off`,
`signal/nostr module`, `gitleaks` and `Analyze (Go)` (CodeQL). CodeQL and
dependency review run only while the `CODE_SCANNING` variable is set — see
[devsecops.md](devsecops.md). Where it is not set they are skipped, and a
skipped required check counts as passing, so they enforce nothing there.

`bypass_actors` is empty on every ruleset, on purpose: the rules bind
administrators too. A workflow that needs to write to `main` — only
`handoff.yml` does — commits through the GitHub API, which signs what it
creates, rather than asking for a bypass.

Release tags are protected separately: `refs/tags/v*.*.*` and
`refs/tags/signal/nostr/v*.*.*` cannot be created over, moved or deleted once
they exist, by anyone. A published version is in the Go module proxy and
checksum database for good, so a tag that could move would break every build
that fetched the old one.

Both halves of that sentence were weaker than it read until 2026-10-29. The
ruleset carried `deletion` and `non_fast_forward` but not `update`, and a tag
moved forward along the same line is a fast-forward, so a release tag could in
fact be moved; and the pattern matched only the root module, leaving the
already-published `signal/nostr/v0.1.0` with no protection at all. Both are
fixed in the ruleset and in `tags-protected.json`.

### The merge method, and signed commits

`main` requires signed commits today, and the only allowed merge method is a
merge commit, which GitHub creates and signs.

This is the agreed direction as of 2026-10-29, for all four repositories, and
what this one already enforced. Squash is rejected because GitHub's squash
commit drops the individual DCO attestations. Rebase is rejected because it
cannot carry signatures: GitHub rewrites each commit of a rebase merge and has
no key of the author's, so with rebase as the only merge method every commit
would land unsigned, which is exactly what `required_signatures` refuses.

The direction recorded here until 2026-10-29 was the opposite — rebase with
linear history, signatures off — and the gap in it is worth keeping. Rebase was
argued against *squash*, on the DCO attestations. Against a merge commit it had
no argument beyond the shape of `git log`: a merge preserves the commits
underneath, with their signatures and their trailers intact, and the DCO check
already skips merges (`git log --no-merges`). A DCO trailer is an attestation of
origin, not a cryptographic signature; anyone can type the line. Measured on
2026-10-29, the trunks that merged by rebase had 2 of 100 and 1 of 31 commits
verified, against 100 of 100 here — bots included, because GitHub signs what it
creates through the API.

The price is merge bubbles: 28 of the last 100 commits here are merges.
`git log --first-parent` reads the trunk without them.

One consequence is worth knowing before it bites, because it already has.
While `required_signatures` is on, **`gh pr update-branch --rebase` and
"Update with rebase" break the pull request they are meant to help**. They
rewrite the branch's commits server-side, GitHub has no key to sign them with,
and the rebased commits land `unsigned`; the ruleset then refuses the merge with
every check green, which reads as an unexplained block. It happened to #163 on
2026-10-28: its commits were verified before the update and unsigned after.
Bring such a branch up to date locally instead, with
`git rebase -f --gpg-sign <base>`, and force-push it.

The plain `gh pr update-branch`, and the "Update branch" button in its default
mode, are safe: they merge the base into the branch, and GitHub signs the merge
commit it creates. Verified on 2026-10-29 against a dependabot pull request in
chirp-status — both existing commits stayed `verified` and the new merge commit
was `verified` too. An earlier revision of this file said the button itself was
unsafe; that was wrong, and only the rebase variants are.

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

That rule now has a mechanism. The `hold: design` label fails the required
`Design hold` check, so a pull request carrying it cannot be merged however
green the rest is. It is for a change waiting on a decision rather than on
review — the relay network's operating architecture is the standing example.
The maintainer puts the label on and takes it off; the check re-runs by itself
when the label changes, so lifting a hold needs no push. A draft would not do:
it is one click from being undone, and it tells an agent that has not read the
discussion nothing at all. The design and documentation pull requests a hold
waits on are never held themselves — they are the condition for lifting it.

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
