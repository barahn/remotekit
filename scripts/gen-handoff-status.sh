#!/usr/bin/env bash
#
# Rewrites the generated block in HANDOFF.md with the facts a handoff goes stale
# on: main's tip, and every open pull request with the verdict of the CI run on
# its head commit. Everything else in that file is prose a script has no
# business writing.
#
# Needs gh authenticated (GH_TOKEN in CI) and jq. Run from anywhere in the repo.
set -euo pipefail

BEGIN='<!-- BEGIN GENERATED: handoff status -->'
END='<!-- END GENERATED: handoff status -->'

cd "$(git rev-parse --show-toplevel)"
repo=$(gh repo view --json nameWithOwner -q .nameWithOwner)
default=$(gh repo view --json defaultBranchRef -q .defaultBranchRef.name)

body=$(mktemp)
trap 'rm -f "$body"' EXIT

{
  printf '%s\n\n' "$BEGIN"
  # Deliberately no timestamp: it would differ on every run, so the workflow
  # would commit on every push to main even when nothing about the state
  # changed. `git log -1 -- HANDOFF.md` answers "how fresh is this".
  printf '_Generated from `%s` by `scripts/gen-handoff-status.sh`._\n\n' "$repo"

  # The tip is read from the API rather than the checkout, so the block says
  # what the branch actually is even when run from a stale or shallow clone.
  read -r sha subject < <(
    gh api "repos/$repo/commits/$default" \
      -q '[.sha[0:7], (.commit.message | split("\n")[0])] | @tsv'
  )
  printf '**`%s` is at `%s`** — %s\n\n' "$default" "$sha" "$subject"

  prs=$(gh pr list --state open --json number,title,url,headRefName,headRefOid,isDraft \
    --limit 100 | jq -c 'sort_by(.number) | .[]')

  if [ -z "$prs" ]; then
    printf 'No open pull requests.\n'
  else
    printf '### Open pull requests\n\n'
    printf '| PR | Title | Branch | CI on head |\n'
    printf '|---|---|---|---|\n'
    while IFS= read -r pr; do
      num=$(jq -r .number <<<"$pr")
      title=$(jq -r '.title | gsub("\\|"; "\\\\|")' <<<"$pr")
      url=$(jq -r .url <<<"$pr")
      branch=$(jq -r .headRefName <<<"$pr")
      oid=$(jq -r .headRefOid <<<"$pr")
      [ "$(jq -r .isDraft <<<"$pr")" = true ] && title="$title _(draft)_"

      # A pull request with no check run at all is not the same as a green one,
      # and a handoff that conflates them sends the next agent in blind.
      runs=$(gh api "repos/$repo/commits/$oid/check-runs" \
        -q '.check_runs[] | [.name, .status, (.conclusion // "pending"), .html_url] | @tsv' \
        2>/dev/null || true)
      if [ -z "$runs" ]; then
        ci='no checks reported'
      else
        ci=$(
          while IFS=$'\t' read -r name status conclusion html; do
            case "$conclusion" in
              success) printf '%s ✅<br>' "$name" ;;
              pending) printf '%s ⏳ (%s)<br>' "$name" "$status" ;;
              *)       printf '[%s ❌ %s](%s)<br>' "$name" "$conclusion" "$html" ;;
            esac
          done <<<"$runs"
        )
        ci=${ci%<br>}
      fi
      printf '| [#%s](%s) | %s | `%s` | %s |\n' "$num" "$url" "$title" "$branch" "$ci"
    done <<<"$prs"
  fi

  printf '\n%s\n' "$END"
} >"$body"

# Splice the block in, leaving every other line of the file untouched.
python3 - "$body" <<'PY'
import sys, pathlib
begin = '<!-- BEGIN GENERATED: handoff status -->'
end = '<!-- END GENERATED: handoff status -->'
path = pathlib.Path('HANDOFF.md')
text = path.read_text()
new = pathlib.Path(sys.argv[1]).read_text().rstrip('\n')
i, j = text.index(begin), text.index(end) + len(end)
path.write_text(text[:i] + new + text[j:])
PY

echo "HANDOFF.md status block regenerated."
