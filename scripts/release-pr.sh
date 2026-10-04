#!/usr/bin/env bash
#
# Opens or updates the release pull request: develop into main.
#
# The pull request is headed by develop itself and is merged with a merge
# commit, which GitHub creates and signs. develop's commits therefore become
# part of main, and once sync-develop.sh has fast-forwarded develop to that
# merge, main is behind develop by exactly what has not been released yet.
#
# Needs gh authenticated (GH_TOKEN in CI) and origin/main and origin/develop
# fetched. With DRY_RUN=1 it reads everything and prints the writes instead
# of making them.
set -euo pipefail
# The title is cut by characters, and it carries → and —.
export LC_ALL=C.UTF-8

REPO=${REPO:?set REPO to owner/name}
REVIEWER=${REVIEWER:-mendsec}
DRY_RUN=${DRY_RUN:-}
owner=${REPO%%/*}

write() {
  if [ -n "$DRY_RUN" ]; then
    printf 'DRY_RUN:' >&2
    printf ' %q' "$@" >&2
    printf '\n' >&2
  else
    "$@"
  fi
}

# Content, not commit count, decides: the sync merge that lands on develop
# when it moved during a release changes no file, and alone it is no release.
if [ "$(git rev-parse 'origin/develop^{tree}')" = "$(git rev-parse 'origin/main^{tree}')" ]; then
  echo "develop's tree is main's; nothing to release."
  exit 0
fi

count=$(git rev-list --count origin/main..origin/develop)
log=$(git log --format='- %h %s' origin/main..origin/develop)
latest=$(git log -1 --format=%s origin/develop)

title="chore(release): merge develop → main ($count commits) — $latest"
if [ "${#title}" -gt 120 ]; then
  title="${title:0:117}..."
fi

body=$(cat <<'EOF'
## 🚀 Automated Release PR: `develop` → `main`

Automated Pull Request created by `github-actions[bot]` to propose merging accumulated development changes into production.

### 📊 Summary
- **Commits to merge**: `@COUNT@`
- **Head Branch**: `develop`
- **Base Branch**: `main`
- **Latest Change**: @LATEST@

### 📝 Commit Log
@LOG@

### 🧪 Quality checks
These run on this pull request. Their verdict is in the Checks tab,
not in this list, which is written before any of them has finished.
- Release source: the head is `develop`
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

> 🔀 **Merge method**: **Create a merge commit**, the only one `main` accepts.
EOF
)
# Quoted replacements: bash 5.2 otherwise reads & in a commit subject as the
# matched text.
body=${body//@COUNT@/"$count"}
body=${body//@LATEST@/"$latest"}
body=${body//@LOG@/"$log"}

pr=$(gh api "repos/$REPO/pulls?state=open&base=main&head=$owner:develop" --jq '.[0].number // empty')
if [ -z "$pr" ]; then
  if [ -n "$DRY_RUN" ]; then
    pr=DRY-RUN-PR
    printf 'DRY_RUN: open pull request develop -> main\n' >&2
  else
    pr=$(gh api --method POST "repos/$REPO/pulls" \
      -f base=main -f head=develop -f title="$title" -f body="$body" --jq .number)
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
