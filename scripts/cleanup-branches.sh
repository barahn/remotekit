#!/usr/bin/env bash
#
# scripts/cleanup-branches.sh
#
# Cleans up local and remote Git branches that are no longer needed:
# - Local branches whose upstream tracking branch is gone ([gone]).
# - Local branches already merged into the integration/release branch (develop/main).
# - Remote branches on origin whose pull requests have already been merged or
#   whose commits are fully merged into the default branch.
#
# Always protects critical branches: main, develop, and the current branch.
#
# Usage:
#   ./scripts/cleanup-branches.sh [options]
#
# Options:
#   --dry-run, -n      Show what would be cleaned without deleting anything
#   --local, -l        Clean only local branches (default if no mode specified)
#   --remote, -r       Clean only remote branches on origin
#   --all, -a          Clean both local and remote branches
#   --force, -y        Do not prompt for interactive confirmation
#   --base <branch>    Base branch to compare against (default: auto-detected default branch)
#   --help, -h         Show this help message

set -euo pipefail

# ANSI colors (disabled if stdout is not a tty)
if [ -t 1 ]; then
  BOLD='\033[1m'
  GREEN='\033[0;32m'
  YELLOW='\033[0;33m'
  RED='\033[0;31m'
  BLUE='\033[0;34m'
  RESET='\033[0m'
else
  BOLD=''
  GREEN=''
  YELLOW=''
  RED=''
  BLUE=''
  RESET=''
fi

DRY_RUN=false
CLEAN_LOCAL=false
CLEAN_REMOTE=false
FORCE=false
CUSTOM_BASE=""

show_help() {
  cat <<EOF
${BOLD}Usage:${RESET} $0 [options]

${BOLD}Options:${RESET}
  --dry-run, -n      Show branches that would be deleted without performing deletion
  --local, -l        Clean local merged branches and branches whose upstream is [gone]
  --remote, -r       Clean remote branches on origin whose PRs are merged
  --all, -a          Clean both local and remote branches
  --force, -y        Skip confirmation prompts and delete immediately
  --base <branch>    Override base branch (defaults to repo default branch, e.g. develop)
  --help, -h         Display this help message

${BOLD}Examples:${RESET}
  $0 --dry-run
  $0 --local
  $0 --remote --dry-run
  $0 --all
EOF
}

# Parse CLI arguments
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run|-n)
      DRY_RUN=true
      shift
      ;;
    --local|-l)
      CLEAN_LOCAL=true
      shift
      ;;
    --remote|-r)
      CLEAN_REMOTE=true
      shift
      ;;
    --all|-a)
      CLEAN_LOCAL=true
      CLEAN_REMOTE=true
      shift
      ;;
    --force|-y)
      FORCE=true
      shift
      ;;
    --base)
      CUSTOM_BASE="$2"
      shift 2
      ;;
    --help|-h)
      show_help
      exit 0
      ;;
    *)
      echo -e "${RED}Unknown option:${RESET} $1" >&2
      show_help
      exit 1
      ;;
  esac
done

# If neither --local nor --remote was explicitly set, default to --local
if [ "$CLEAN_LOCAL" = false ] && [ "$CLEAN_REMOTE" = false ]; then
  CLEAN_LOCAL=true
fi

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [ -z "$REPO_ROOT" ]; then
  echo -e "${RED}Error:${RESET} Must be run inside a Git repository." >&2
  exit 1
fi
cd "$REPO_ROOT"

# Determine default branch
if [ -n "$CUSTOM_BASE" ]; then
  DEFAULT_BRANCH="$CUSTOM_BASE"
elif command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
  DEFAULT_BRANCH="$(gh repo view --json defaultBranchRef -q .defaultBranchRef.name 2>/dev/null || echo "develop")"
else
  DEFAULT_BRANCH="develop"
fi

CURRENT_BRANCH="$(git branch --show-current 2>/dev/null || echo "")"

is_protected() {
  local b="$1"
  case "$b" in
    main|master|develop|"$DEFAULT_BRANCH") return 0 ;;
    *) return 1 ;;
  esac
}

echo -e "${BOLD}Branch Cleanup Utility${RESET}"
echo -e "Repository root: ${BLUE}$REPO_ROOT${RESET}"
echo -e "Default branch:  ${BLUE}$DEFAULT_BRANCH${RESET}"
echo -e "Current branch:  ${BLUE}${CURRENT_BRANCH:-detached}${RESET}"
if [ "$DRY_RUN" = true ]; then
  echo -e "Mode:            ${YELLOW}DRY-RUN (no branches will be deleted)${RESET}"
fi
echo ""

# 1. Prune remote tracking references
echo -e "${BOLD}[1/3] Fetching and pruning remote references...${RESET}"
git fetch origin --prune --quiet || true

# 2. Local branch cleanup
if [ "$CLEAN_LOCAL" = true ]; then
  echo ""
  echo -e "${BOLD}[2/3] Analyzing local branches...${RESET}"
  LOCAL_TO_DELETE=()

  # A. Branches whose upstream is gone
  while IFS= read -r branch; do
    [ -z "$branch" ] && continue
    if is_protected "$branch" || [ "$branch" = "$CURRENT_BRANCH" ]; then
      continue
    fi
    LOCAL_TO_DELETE+=("$branch (upstream gone)")
  done < <(git for-each-ref --format '%(refname:short) %(upstream:track)' refs/heads | awk '$2 == "[gone]" {print $1}')

  # B. Branches merged into default branch or main
  for base_ref in "$DEFAULT_BRANCH" "main"; do
    if git show-ref --verify --quiet "refs/heads/$base_ref"; then
      while IFS= read -r branch; do
        branch="$(echo "$branch" | tr -d ' *')"
        [ -z "$branch" ] && continue
        if is_protected "$branch" || [ "$branch" = "$CURRENT_BRANCH" ]; then
          continue
        fi
        # Avoid duplicate entries
        already_added=false
        for item in "${LOCAL_TO_DELETE[@]}"; do
          if [[ "$item" == "$branch "* ]] || [ "$item" = "$branch" ]; then
            already_added=true
            break
          fi
        done
        if [ "$already_added" = false ]; then
          LOCAL_TO_DELETE+=("$branch (merged into $base_ref)")
        fi
      done < <(git branch --merged "$base_ref" 2>/dev/null || true)
    fi
  done

  if [ ${#LOCAL_TO_DELETE[@]} -eq 0 ]; then
    echo -e "${GREEN}No local branches to clean.${RESET}"
  else
    echo -e "Found ${#LOCAL_TO_DELETE[@]} local branch(es) eligible for deletion:"
    for item in "${LOCAL_TO_DELETE[@]}"; do
      echo -e "  - ${YELLOW}$item${RESET}"
    done

    if [ "$DRY_RUN" = false ]; then
      CONFIRM=false
      if [ "$FORCE" = true ]; then
        CONFIRM=true
      else
        read -r -p "Delete these local branches? [y/N] " resp
        case "$resp" in
          [yY][eE][sS]|[yY]) CONFIRM=true ;;
          *) CONFIRM=false ;;
        esac
      fi

      if [ "$CONFIRM" = true ]; then
        for item in "${LOCAL_TO_DELETE[@]}"; do
          branch_name="${item%% *}"
          # Try soft delete first, fallback to -D if merged upstream (e.g. squash merge)
          if git branch -d "$branch_name" >/dev/null 2>&1; then
            echo -e "  ${GREEN}Deleted local branch:${RESET} $branch_name"
          elif git branch -D "$branch_name" >/dev/null 2>&1; then
            echo -e "  ${GREEN}Force-deleted local branch (squash/rebase merged):${RESET} $branch_name"
          else
            echo -e "  ${RED}Failed to delete local branch:${RESET} $branch_name"
          fi
        done
      else
        echo -e "${YELLOW}Skipped local branch deletion.${RESET}"
      fi
    fi
  fi
fi

# 3. Remote branch cleanup
if [ "$CLEAN_REMOTE" = true ]; then
  echo ""
  echo -e "${BOLD}[3/3] Analyzing remote branches on origin...${RESET}"

  if ! command -v gh >/dev/null 2>&1; then
    echo -e "${RED}Error:${RESET} GitHub CLI (gh) is required for safe remote branch analysis." >&2
    exit 1
  fi

  REPO_NAME="$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null || echo "")"
  if [ -z "$REPO_NAME" ]; then
    echo -e "${RED}Error:${RESET} Unable to determine GitHub repository name via gh." >&2
    exit 1
  fi

  # Fetch all PRs once in a single query for speed
  local_prs_json="$(gh pr list --repo "$REPO_NAME" --state all --limit 500 --json number,headRefName,state 2>/dev/null || echo "[]")"

  # Fetch all remote branches
  REMOTE_BRANCHES="$(gh api "repos/$REPO_NAME/branches" --paginate -q '.[].name' 2>/dev/null || true)"

  REMOTE_TO_DELETE=()

  while IFS= read -r branch; do
    [ -z "$branch" ] && continue
    if is_protected "$branch"; then
      continue
    fi

    # Check if there is an active open PR for this branch
    open_prs="$(jq -r --arg b "$branch" '.[] | select(.headRefName == $b and .state == "OPEN") | .number' <<<"$local_prs_json")"
    if [ -n "$open_prs" ]; then
      # Active PR exists; do not delete!
      continue
    fi

    # Check if branch has merged PRs
    merged_prs="$(jq -r --arg b "$branch" '.[] | select(.headRefName == $b and .state == "MERGED") | .number' <<<"$local_prs_json")"

    is_merged=false
    reason=""
    if [ -n "$merged_prs" ]; then
      is_merged=true
      merged_list="$(echo "$merged_prs" | paste -sd, - | sed 's/,/, #/g')"
      reason="PR #${merged_list} merged"
    elif git rev-parse --verify --quiet "origin/$DEFAULT_BRANCH" >/dev/null && \
         git rev-parse --verify --quiet "origin/$branch" >/dev/null && \
         git merge-base --is-ancestor "origin/$branch" "origin/$DEFAULT_BRANCH" 2>/dev/null; then
      is_merged=true
      reason="ancestor of $DEFAULT_BRANCH"
    fi

    if [ "$is_merged" = true ]; then
      REMOTE_TO_DELETE+=("$branch ($reason)")
    fi
  done <<<"$REMOTE_BRANCHES"

  if [ ${#REMOTE_TO_DELETE[@]} -eq 0 ]; then
    echo -e "${GREEN}No remote branches to clean on origin.${RESET}"
  else
    echo -e "Found ${#REMOTE_TO_DELETE[@]} remote branch(es) eligible for deletion on origin:"
    for item in "${REMOTE_TO_DELETE[@]}"; do
      echo -e "  - ${YELLOW}$item${RESET}"
    done

    if [ "$DRY_RUN" = false ]; then
      CONFIRM=false
      if [ "$FORCE" = true ]; then
        CONFIRM=true
      else
        read -r -p "Delete these branches on remote origin? [y/N] " resp
        case "$resp" in
          [yY][eE][sS]|[yY]) CONFIRM=true ;;
          *) CONFIRM=false ;;
        esac
      fi

      if [ "$CONFIRM" = true ]; then
        for item in "${REMOTE_TO_DELETE[@]}"; do
          branch_name="${item%% *}"
          if gh api --method DELETE "repos/$REPO_NAME/git/refs/heads/$branch_name" >/dev/null 2>&1; then
            echo -e "  ${GREEN}Deleted remote branch:${RESET} $branch_name"
          else
            echo -e "  ${RED}Failed to delete remote branch:${RESET} $branch_name"
          fi
        done
        # Prune local tracking refs after remote deletion
        git fetch origin --prune --quiet || true
      else
        echo -e "${YELLOW}Skipped remote branch deletion.${RESET}"
      fi
    fi
  fi
fi

echo ""
echo -e "${BOLD}${GREEN}Done!${RESET}"
