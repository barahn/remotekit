#!/usr/bin/env bash
#
# Brings develop up to main after the release pull request merges.
#
# A release is develop merged into main with a merge commit, so main is then
# develop plus that merge. develop is fast-forwarded to it, and the two
# branches are identical until the next change lands on develop. When develop
# moved while the release was open -- the handoff refresh lands right after a
# merge -- the fast-forward is refused and main is merged into develop
# instead; that merge holds nothing new, since main's only own commit is the
# merge of develop.
#
# Everything goes through the REST API: develop requires signed commits, and
# GitHub signs the merge it creates there.
#
# Needs gh authenticated (GH_TOKEN in CI). With DRY_RUN=1 it reads everything
# and prints the writes instead of making them.
set -euo pipefail

REPO=${REPO:?set REPO to owner/name}
DRY_RUN=${DRY_RUN:-}

main=$(gh api "repos/$REPO/git/ref/heads/main" --jq .object.sha)

if [ -n "$DRY_RUN" ]; then
  echo "DRY_RUN: fast-forward develop to ${main:0:7}, or POST repos/$REPO/merges base=develop head=main" >&2
  exit 0
fi

# force=false: the ref only moves if main descends from develop.
if gh api --method PATCH "repos/$REPO/git/refs/heads/develop" \
    -f sha="$main" -F force=false --silent 2>/dev/null; then
  echo "develop fast-forwarded to main (${main:0:7})."
  exit 0
fi

echo "develop moved during the release; merging main into it."
# A 204 means develop already contains main; a 409 is a real conflict and
# fails the job.
gh api --method POST "repos/$REPO/merges" -f base=develop -f head=main --silent \
  -f commit_message="Merge main into develop after the release

Signed-off-by: github-actions[bot] <41898282+github-actions[bot]@users.noreply.github.com>"
echo "main merged into develop."
