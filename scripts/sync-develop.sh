#!/usr/bin/env bash
#
# Brings main back into develop after a release pull request merges, so that
# develop keeps containing main -- which release-pr.sh requires before it
# publishes develop's tree again.
#
# A release from release/next carries develop's tree as it was when
# release-pr.sh last ran: the commit its Develop-Head trailer names. develop
# has nearly always moved on since, if only by a handoff refresh, and a
# three-way merge of main into develop then conflicts in HANDOFF.md's
# generated block, which both sides rewrote. Yet nothing main has is news to
# develop in that case: main's tree is the tree of a develop commit develop
# descends from. So the merge keeps develop's tree and only records main as a
# parent.
#
# Anything else -- a release headed by develop, as release-pr.yml opened them
# before, or a main carrying more than the release -- gets the ordinary
# three-way merge from POST /merges, which fails on a real conflict.
#
# Every commit goes through the REST API with no author or committer, so
# GitHub signs it; develop and main both require signed commits.
#
# Needs gh authenticated (GH_TOKEN in CI) and git for parsing trailers, but no
# history: everything is read through the API. REPO, HEAD_REF and HEAD_SHA
# describe the merged pull request's head. With DRY_RUN=1 it reads everything
# and prints the writes instead of making them.
set -euo pipefail

REPO=${REPO:?set REPO to owner/name}
HEAD_REF=${HEAD_REF:?set HEAD_REF to the merged pull request head branch}
HEAD_SHA=${HEAD_SHA:?set HEAD_SHA to the merged pull request head commit}
RELEASE_BRANCH=${RELEASE_BRANCH:-release/next}
DRY_RUN=${DRY_RUN:-}

tree_of() {
  gh api "repos/$REPO/git/commits/$1" --jq .tree.sha
}

# GET /compare/A...B is "ahead" or "identical" exactly when A is an ancestor of B.
contains() {
  case "$(gh api "repos/$REPO/compare/$2...$1" --jq .status)" in
    ahead | identical) return 0 ;;
    *) return 1 ;;
  esac
}

signoff='Signed-off-by: github-actions[bot] <41898282+github-actions[bot]@users.noreply.github.com>'
main=$(gh api "repos/$REPO/git/ref/heads/main" --jq .object.sha)

develop_head=
if [ "$HEAD_REF" = "$RELEASE_BRANCH" ]; then
  develop_head=$(gh api "repos/$REPO/git/commits/$HEAD_SHA" --jq .message |
    git interpret-trailers --parse | sed -n 's/^Develop-Head: //p' | tail -n1)
fi

if [ -z "$develop_head" ] || [ "$(tree_of "$main")" != "$(tree_of "$develop_head")" ]; then
  echo "main is not exactly a release of develop; using a three-way merge."
  # A 204 means develop already contains main; a 409 is a real conflict and
  # fails the job.
  if [ -n "$DRY_RUN" ]; then
    echo "DRY_RUN: POST repos/$REPO/merges base=develop head=main" >&2
  else
    gh api --method POST "repos/$REPO/merges" -f base=develop -f head=main --silent \
      -f commit_message="Merge main into develop after the release

$signoff"
  fi
  exit 0
fi

# develop can move while this runs -- the handoff refresh lands right after a
# release -- and the ref update below refuses anything but a fast-forward, so a
# lost race rereads the tip and tries again.
for attempt in 1 2 3 4; do
  develop=$(gh api "repos/$REPO/git/ref/heads/develop" --jq .object.sha)

  if contains "$develop" "$main"; then
    echo "develop (${develop:0:7}) already contains main (${main:0:7})."
    exit 0
  fi
  if ! contains "$develop" "$develop_head"; then
    echo "::error::develop (${develop:0:7}) does not descend from ${develop_head:0:7}, the develop commit main was released from. Merge main into develop by hand."
    exit 1
  fi

  message="Merge main into develop after the release

main is develop at ${develop_head:0:7}, which develop already contains, so
develop's tree is kept as it is.

$signoff"

  if [ -n "$DRY_RUN" ]; then
    printf 'DRY_RUN: create commit tree=%s parents=%s,%s, then move develop to it\n--- message ---\n%s\n--- end ---\n' \
      "$(tree_of "$develop")" "$develop" "$main" "$message" >&2
    exit 0
  fi

  commit=$(jq -n --arg message "$message" --arg tree "$(tree_of "$develop")" \
      --arg develop "$develop" --arg main "$main" \
      '{message: $message, tree: $tree, parents: [$develop, $main]}' |
    gh api "repos/$REPO/git/commits" --input - --jq .sha)

  # develop's ruleset would refuse an unsigned commit anyway; this says why.
  verified=$(gh api "repos/$REPO/commits/$commit" --jq '.commit.verification | "\(.verified) \(.reason)"')
  if [ "${verified%% *}" != true ]; then
    echo "::error::GitHub did not sign the sync commit $commit (${verified#* }); develop left as it was."
    exit 1
  fi

  if gh api --method PATCH "repos/$REPO/git/refs/heads/develop" \
      -f sha="$commit" -F force=false --silent; then
    echo "develop now at ${commit:0:7}, with main (${main:0:7}) as its second parent."
    exit 0
  fi
  echo "develop moved during the sync (attempt $attempt); trying again on its new tip."
  sleep $((2 ** attempt))
done
echo "::error::Could not move develop after 4 attempts."
exit 1
