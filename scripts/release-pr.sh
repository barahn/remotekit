#!/usr/bin/env bash
#
# Opens or updates the release pull request into main.
#
# Its head is not develop but RELEASE_BRANCH: one commit on top of main whose
# tree is develop's, created through the REST API so GitHub signs it. main
# requires verified signatures and linear history, so releases are
# squash-merged and develop's own commits never become part of main. A pull
# request headed by develop therefore lists all of develop's history on every
# release, and that list only grows -- with it the bot commits the handoff
# workflow pushed unsigned before it moved to the API, which nothing can sign
# after the fact without rewriting develop. Squashed, a release gives main
# one tree of develop's either way; carrying it in one signed commit makes the
# pull request show exactly that. sync-develop.sh then records main in
# develop's history without merging three ways.
#
# Needs gh authenticated (GH_TOKEN in CI), jq, and origin/main and
# origin/develop fetched. Uses the REST API only. With DRY_RUN=1 it reads
# everything and prints the writes instead of making them.
set -euo pipefail
# The title is cut by characters, and it carries → and —.
export LC_ALL=C.UTF-8

REPO=${REPO:?set REPO to owner/name}
RELEASE_BRANCH=${RELEASE_BRANCH:-release/next}
REVIEWER=${REVIEWER:-mendsec}
DRY_RUN=${DRY_RUN:-}
owner=${REPO%%/*}

# The develop commit a release commit was made from, read from its message.
develop_head() {
  git interpret-trailers --parse | sed -n 's/^Develop-Head: //p' | tail -n1
}

write() {
  if [ -n "$DRY_RUN" ]; then
    printf 'DRY_RUN:' >&2
    printf ' %q' "$@" >&2
    printf '\n' >&2
  else
    "$@"
  fi
}

main=$(git rev-parse origin/main)
develop=$(git rev-parse origin/develop)
tree=$(git rev-parse "origin/develop^{tree}")

# Publishing develop's tree is only right while develop contains main:
# otherwise anything main has and develop lacks would be reverted by the
# release. sync-develop.yml merges main back after each release; this fails
# when that sync did, or when something other than a release reached main,
# which nothing merges back.
if ! git merge-base --is-ancestor "$main" "$develop"; then
  echo "::error::main ($main) is not contained in develop ($develop). Merge main into develop first; sync-develop.yml does that only after a release."
  exit 1
fi

if [ "$tree" = "$(git rev-parse "origin/main^{tree}")" ]; then
  echo "develop's tree is main's; nothing to release."
  exit 0
fi

# What release/next carries now, if it exists.
# gh prints the error body to stdout on a 404, so the exit status decides.
if ! current=$(gh api "repos/$REPO/git/ref/heads/$RELEASE_BRANCH" --jq .object.sha 2>/dev/null); then
  current=
fi
current_parent=
current_head=
if [ -n "$current" ]; then
  current_parent=$(gh api "repos/$REPO/git/commits/$current" --jq '.parents[0].sha')
  current_head=$(gh api "repos/$REPO/git/commits/$current" --jq .message | develop_head)
fi

# A re-run of an older push reads develop as it was then. It must not move
# release/next back over a newer develop that a later run already published.
if [ "$current_parent" = "$main" ] && [ -n "$current_head" ] && [ "$current_head" != "$develop" ] &&
  git merge-base --is-ancestor "$develop" "$current_head" 2>/dev/null; then
  echo "$RELEASE_BRANCH already carries develop at ${current_head:0:7}, newer than ${develop:0:7}; leaving it."
  exit 0
fi

# The commit log starts where the last release left off: the develop commit a
# release pull request headed by develop was at, or the one a release commit
# names in its Develop-Head trailer. Without either, it falls back to
# everything develop has that main lacks.
since=$main
last=$(gh api "repos/$REPO/pulls?state=closed&base=main&per_page=100" \
  --jq "[.[] | select(.merged_at != null and (.head.ref == \"develop\" or .head.ref == \"$RELEASE_BRANCH\"))]
        | sort_by(.merged_at) | last // empty | [.head.ref, .head.sha] | @tsv")
if [ -n "$last" ]; then
  read -r last_ref last_sha <<<"$last"
  if [ "$last_ref" != develop ]; then
    last_sha=$(gh api "repos/$REPO/git/commits/$last_sha" --jq .message | develop_head)
  fi
  if [ -n "$last_sha" ] && git merge-base --is-ancestor "$last_sha" "$develop" 2>/dev/null; then
    since=$last_sha
  fi
fi
if [ "$since" = "$main" ]; then
  echo "::notice::No previous release found on develop; listing everything develop has that main lacks."
fi

# main's own commits reach develop through the sync merge; they are not news.
count=$(git rev-list --count "$develop" --not "$since" "$main")
log=$(git log --format='- %h %s' "$develop" --not "$since" "$main")
latest=$(git log -1 --format=%s "$develop")

title="chore(release): merge develop → main ($count commits) — $latest"
if [ "${#title}" -gt 120 ]; then
  title="${title:0:117}..."
fi

# Reuse the release commit when it already carries this develop on this main,
# so a rerun neither moves the branch nor runs CI again for nothing.
release=
if [ "$current_parent" = "$main" ] && [ "$current_head" = "$develop" ]; then
  release=$current
  echo "$RELEASE_BRANCH already carries develop at ${develop:0:7} on main at ${main:0:7}."
  # Unless the run that made it never got as far as dispatching CI.
  if [ "$(gh api "repos/$REPO/commits/$release/check-runs" --jq .total_count)" = 0 ]; then
    write gh api --method POST "repos/$REPO/actions/workflows/ci.yml/dispatches" \
      -f ref="$RELEASE_BRANCH" --silent
  fi
fi

if [ -z "$release" ]; then
  # No author or committer is given: GitHub signs a commit created through the
  # API only when it is the identity making the request. The trailers are the
  # last paragraph so git reads them as trailers.
  message="chore(release): develop → main ($count commits)

The tree of develop at ${develop:0:7}, as one commit on main.

$log

Develop-Head: $develop
Signed-off-by: github-actions[bot] <41898282+github-actions[bot]@users.noreply.github.com>"

  if [ -n "$DRY_RUN" ]; then
    release=DRY-RUN-COMMIT
    printf 'DRY_RUN: create commit tree=%s parent=%s\n--- message ---\n%s\n--- end ---\n' \
      "$tree" "$main" "$message" >&2
  else
    release=$(jq -n --arg message "$message" --arg tree "$tree" --arg parent "$main" \
        '{message: $message, tree: $tree, parents: [$parent]}' |
      gh api "repos/$REPO/git/commits" --input - --jq .sha)
    # The point of the branch is a head GitHub shows as verified. Should the
    # API ever stop signing, fail here, before the ref moves, rather than
    # publish an unsigned release commit.
    verified=$(gh api "repos/$REPO/commits/$release" --jq '.commit.verification | "\(.verified) \(.reason)"')
    if [ "${verified%% *}" != true ]; then
      echo "::error::GitHub did not sign the release commit $release (${verified#* }); $RELEASE_BRANCH left as it was."
      exit 1
    fi
  fi

  if [ -n "$current" ]; then
    write gh api --method PATCH "repos/$REPO/git/refs/heads/$RELEASE_BRANCH" \
      -f sha="$release" -F force=true --silent
  else
    write gh api --method POST "repos/$REPO/git/refs" \
      -f ref="refs/heads/$RELEASE_BRANCH" -f sha="$release" --silent
  fi
  echo "$RELEASE_BRANCH now at ${release:0:7}: develop at ${develop:0:7} on main at ${main:0:7}."

  # The commit and the ref were made with GITHUB_TOKEN, which starts no
  # workflow, so the pull request's head would carry no checks. A dispatch is
  # exempt from that rule.
  write gh api --method POST "repos/$REPO/actions/workflows/ci.yml/dispatches" \
    -f ref="$RELEASE_BRANCH" --silent
fi

body=$(cat <<'EOF'
## 🚀 Automated Release PR: `develop` → `main`

Automated Pull Request created by `github-actions[bot]` to propose merging accumulated development changes into production.

### 📊 Summary
- **Commits since the last release**: `@COUNT@`
- **Head Branch**: `@BRANCH@` — one commit, signed by GitHub, whose tree is `develop` at `@DEVELOP@`
- **Base Branch**: `main`
- **Latest Change**: @LATEST@

### 🔏 Why one commit
`main` requires verified signatures and linear history, so a release is
squash-merged and `develop`'s own commits never become part of `main`. Headed
by `develop`, this pull request would list all of `develop`'s history since it
split from `main`, every time, including the handoff refreshes pushed unsigned
before that workflow moved to the API. So it carries `develop`'s tree at
`@DEVELOP@` as a single commit GitHub creates and signs; anything `develop`
gains after that waits for the next release. The commits below are what that
tree adds since the last release. Changes from people reached `develop`
through pull requests; the handoff refreshes and the post-release sync are bot
commits made directly.

### 📝 Commit Log (since the last release)
@LOG@

### 🧪 Quality checks
These run on this pull request. Their verdict is in the Checks tab,
not in this list, which is written before any of them has finished.
- DCO Sign-off validation (bot-authored commits are exempt)
- NOTICE coverage for vendored code
- Go Build & Vet (`go build ./...`, `go vet ./...`)
- Cross-compile Check (Windows & macOS)
- Linter (`golangci-lint`)
- Unit Tests & VP8 Oracle (`go test -race ./...`)
- Firefox Browser WebRTC Decode Test
- SAST Security Scans (`gosec` / `govulncheck`)
- CodeQL and dependency review

---
> 👥 **Reviewer Requested**: `@mendsec` — Manual review and approval are strictly required before merging into `main`. Auto-merge is disabled.
EOF
)
# Quoted replacements: bash 5.2 otherwise reads & in a commit subject as the
# matched text.
body=${body//@COUNT@/"$count"}
body=${body//@BRANCH@/"$RELEASE_BRANCH"}
body=${body//@DEVELOP@/"${develop:0:7}"}
body=${body//@LATEST@/"$latest"}
body=${body//@LOG@/"$log"}

pr=$(gh api "repos/$REPO/pulls?state=open&base=main&head=$owner:$RELEASE_BRANCH" --jq '.[0].number // empty')
if [ -z "$pr" ]; then
  if [ -n "$DRY_RUN" ]; then
    pr=DRY-RUN-PR
    printf 'DRY_RUN: open pull request %s -> main\n' "$RELEASE_BRANCH" >&2
  else
    pr=$(gh api --method POST "repos/$REPO/pulls" \
      -f base=main -f head="$RELEASE_BRANCH" -f title="$title" -f body="$body" --jq .number)
  fi
  echo "Opened #$pr."
else
  write gh api --method PATCH "repos/$REPO/pulls/$pr" -f title="$title" -f body="$body" --silent
  echo "Updated #$pr."
fi
if [ -n "$DRY_RUN" ]; then
  printf 'DRY_RUN: --- title ---\n%s\n--- body ---\n%s\n--- end ---\n' "$title" "$body" >&2
fi

write gh api --method POST "repos/$REPO/pulls/$pr/requested_reviewers" \
  -f "reviewers[]=$REVIEWER" --silent || echo "Could not request review from $REVIEWER."

# Release pull requests this workflow opened before it had a release branch
# were headed by develop. They still list every unsigned commit, so they are
# closed in favour of this one. Ones a person opened are left alone. The list
# is read first so a failed request stops here rather than being looped over.
olds=$(gh api "repos/$REPO/pulls?state=open&base=main&head=$owner:develop" \
  --jq '.[] | select(.user.login == "github-actions[bot]") | .number')
for old in $olds; do
  write gh api --method POST "repos/$REPO/issues/$old/comments" --silent -f body="Superseded by #$pr, which carries \`develop\`'s tree as one commit GitHub has signed. Headed by \`develop\`, this pull request lists every commit \`develop\` has had since it split from \`main\`, including the handoff refreshes pushed unsigned before that workflow moved to the API; see \`scripts/release-pr.sh\`."
  write gh api --method PATCH "repos/$REPO/pulls/$old" -f state=closed --silent
  echo "Closed #$old in favour of #$pr."
done
