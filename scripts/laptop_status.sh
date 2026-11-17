#!/usr/bin/env bash
# VENDORED from claude-config skills/workflow-preflight @ 13b3e3a9dcef — edit canonical, not this copy
# Mark pushed commits as checked by the laptop pre-push hook (CI trigger policy D9). Two hooks:
#
#   .githooks/pre-push, LAST line, after the hook's own checks passed:
#     scripts/laptop_status.sh "$@"                       # stdin = git's pre-push lines
#   .githooks/reference-transaction, the whole hook:
#     exec scripts/laptop_status.sh --after-push "$@"
#
# Git runs pre-push BEFORE it uploads the commits, and GitHub refuses (HTTP 422) a status on a commit
# it does not have. So pre-push only records the checked shas in <git-dir>/laptop-check-pending, after
# checking that gh can post (else the push stops: owner Q2 as amended 2026-10-28). The posts happen in
# reference-transaction, which git runs when the push updates refs/remotes/* — after the upload. A
# late failure prints NOT MARKED and keeps the sha pending; the next push of a descendant retries it.
# Records only when the pushed sha is HEAD and the tree is clean: the hook checked the checked-out
# tree, so a mark on any other sha would be a lie.
# Env: LAPTOP_STATUS_REPO=o/r (else parsed from the remote URL), LAPTOP_STATUS_GH (gh binary),
# HOOKAUDIT_RECHECK=<sha> (set by `hookaudit --recheck`: the sha is on GitHub, post it at once).
set -euo pipefail

zero=0000000000000000000000000000000000000000
gh_bin="${LAPTOP_STATUS_GH:-gh}"
pending_file="$(git rev-parse --git-dir)/laptop-check-pending"
die() { echo "laptop_status: $*" >&2; exit 1; }

# gh never reads the ref lines: stdin is /dev/null for every call.
post() {  # <slug> <sha> <description>
  "$gh_bin" api -X POST "repos/$1/statuses/$2" -f state=success -f context=laptop/check \
    -f "description=$3" --silent </dev/null
}

if [ "${1:-}" = "--after-push" ]; then
  # reference-transaction: runs on every ref update, so return fast unless there is work.
  if [ "${2:-}" != committed ] || [ ! -s "$pending_file" ]; then
    cat >/dev/null
    exit 0
  fi
  total="$(wc -l < "$pending_file" | tr -d ' ')"
  marked=0
  while read -r _old new ref; do
    case "$ref" in refs/remotes/*) ;; *) continue ;; esac
    [ "$new" != "$zero" ] || continue
    keep="$pending_file.tmp.$$"
    : > "$keep"
    while read -r sha slug tip; do
      if git merge-base --is-ancestor "$sha" "$new" 2>/dev/null </dev/null; then
        if post "$slug" "$sha" "checked at tip $tip"; then
          marked=$((marked + 1))
          continue
        fi
        echo "laptop_status: NOT MARKED $sha on $slug: the push went through, the status post failed." >&2
        echo "  It stays pending and is retried on your next push; or run: hookaudit --recheck $sha" >&2
      fi
      echo "$sha $slug $tip" >> "$keep"
    done < "$pending_file"
    mv "$keep" "$pending_file"
  done
  echo "laptop_status: marked $marked of $total pending laptop/check" >&2
  exit 0
fi

slug="${LAPTOP_STATUS_REPO:-}"
url="${2:-$(git remote get-url "${1:-origin}" 2>/dev/null || true)}"
if [ -z "$slug" ]; then
  slug="$(printf '%s\n' "$url" | sed -nE 's#^(git@github\.com:|ssh://git@github\.com/|https://github\.com/)([^/]+/[^/]+)$#\2#p')"
  slug="${slug%.git}"
  [ -n "$slug" ] || die "cannot read a GitHub owner/repo from remote URL '$url' (set LAPTOP_STATUS_REPO=o/r)"
fi

head="$(git rev-parse HEAD)"
dirty="$(git status --porcelain)"
can_post=0 recorded=0

check_gh() {  # once per push, only when there is something to mark
  [ "$can_post" = 1 ] && return 0
  local repo
  repo="$("$gh_bin" api "repos/$slug" </dev/null 2>&1)" ||
    die "gh cannot read $slug, so the mark could not be posted. Push stopped. Fix gh: 'gh auth status', then 'gh auth login'. ($repo)"
  printf '%s' "$repo" | /usr/bin/grep -Eq '"push": ?true' ||
    die "gh has no push access to $slug, so it cannot post laptop/check. Push stopped. Check 'gh auth status' (account, scopes)."
  can_post=1
}

while read -r local_ref local_sha remote_ref remote_sha; do
  [ -n "${local_sha:-}" ] || continue
  [ "$local_sha" = "$zero" ] && continue                  # deletion: nothing to mark
  case "$remote_ref" in refs/tags/*) continue ;; esac     # tag push: no new commits to mark
  if [ "$local_sha" != "$head" ] || [ -n "$dirty" ]; then
    # F-036: a push run with GIT_CONFIG_GLOBAL=/dev/null drops the global .scratch/ ignore
    [ "$local_sha" = "$head" ] && [ "$dirty" = "?? .scratch/" ] &&
      die "tree dirty only because .scratch/ is untracked: this repo ignores it only through the global git config, which this push skipped. Fix: add .scratch/ to this repo's .gitignore and commit it."
    die "checked tree is not the pushed commit ($local_ref ${local_sha:0:7}, HEAD ${head:0:7}$([ -n "$dirty" ] && echo ', tree dirty')). Check out and push that commit from a clean tree."
  fi
  check_gh
  if [ -n "${HOOKAUDIT_RECHECK:-}" ]; then
    [ "$local_sha" = "$HOOKAUDIT_RECHECK" ] || die "recheck of ${HOOKAUDIT_RECHECK:0:7} but pushing ${local_sha:0:7}"
    post "$slug" "$local_sha" "rechecked at ${local_sha:0:7}" || die "could not post laptop/check on ${local_sha:0:7} to $slug"
    echo "laptop_status: marked ${local_sha:0:7} laptop/check on $slug (recheck)"
    continue
  fi
  if [ "$remote_sha" = "$zero" ]; then
    shas="$(git rev-list "$local_sha" --not --remotes)" || die "git rev-list failed for new branch $remote_ref"
  else
    shas="$(git rev-list "$remote_sha..$local_sha")" || die "remote sha ${remote_sha:0:7} is not in this clone; fetch first"
  fi
  for sha in $shas; do
    line="$sha $slug ${local_sha:0:7}"
    /usr/bin/grep -qxF "$line" "$pending_file" 2>/dev/null || echo "$line" >> "$pending_file"
    recorded=$((recorded + 1))
  done
done

if [ "$recorded" -gt 0 ]; then
  echo "laptop_status: $recorded commit(s) checked; laptop/check is posted after the upload"
  if [ "${1:-}" = "$url" ]; then
    echo "laptop_status: WARNING: pushing to a URL, marks will not be posted (no remote-tracking ref is updated). Push via a named remote, or run hookaudit --recheck <sha> afterwards." >&2
  fi
fi
