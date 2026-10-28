#!/usr/bin/env bash
#
# Generates the block of facts a handoff goes stale on: main's tip, and every
# open pull request with the verdict of the CI run on its head commit.
# Everything else in HANDOFF.md is prose a script has no business writing.
#
# By default the block goes to stdout, which is what CI publishes as the job
# summary. `--in-place` splices it into HANDOFF.md instead, for a local brief.
# CI does not use that mode: main requires a pull request, so no workflow can
# commit to it, and a committed copy of derived state is stale the moment
# anything else lands.
#
# Needs gh authenticated (GH_TOKEN in CI) and jq. Run from anywhere in the repo.
set -euo pipefail

BEGIN='<!-- BEGIN GENERATED: handoff status -->'
END='<!-- END GENERATED: handoff status -->'

in_place=false
case "${1-}" in
  --in-place) in_place=true ;;
  "") ;;
  *) echo "usage: $0 [--in-place]" >&2; exit 2 ;;
esac

cd "$(git rev-parse --show-toplevel)"
repo=$(gh repo view --json nameWithOwner -q .nameWithOwner)
# The trunk by name. The repository's default branch would be the same thing
# only by coincidence, and it was not: the default was still `develop` while
# the trunk was already `main`.
branch=${HANDOFF_BRANCH:-main}

body=$(mktemp)
trap 'rm -f "$body"' EXIT

{
  printf '%s\n\n' "$BEGIN"
  # Still no timestamp. The job summary is dated by the run that produced it,
  # and in `--in-place` mode a timestamp would churn the file on every run.
  printf '_Generated from `%s` by `scripts/gen-handoff-status.sh`._\n\n' "$repo"

  # The tip is read from the API rather than the checkout, so the block says
  # what the branch actually is even when run from a stale or shallow clone.
  read -r sha subject < <(
    gh api "repos/$repo/commits/$branch" \
      -q '[.sha[0:7], (.commit.message | split("\n")[0])] | @tsv'
  )
  printf '**`%s` is at `%s`** — %s\n\n' "$branch" "$sha" "$subject"

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
              # A check that did not apply to this diff is not a failure. The
              # sentinel jobs that stand in for a disabled analysis report
              # `skipped`, and marking them red made every green pull request
              # read as broken.
              skipped|neutral|cancelled)
                       printf '%s — %s<br>' "$name" "$conclusion" ;;
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

if [ "$in_place" = false ]; then
  cat "$body"
  exit 0
fi

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

echo "HANDOFF.md status block regenerated." >&2
