#!/usr/bin/env bash
# Changelog hygiene for the app repos (issuer/, api/, ui/, website/, cli/).
#
# THE FAULT THIS EXISTS FOR: `sdk/scripts/changelog-hygiene.sh` guards the SDK
# packages only. Nothing checked the app repos at all, and at the ADR-101
# release (2026-08-30) they had accumulated several `## Unreleased` headings
# apiece — each working session opening its own — merged by hand at tag time.
# That does not scale and regrows silently (root TODO.md § "App-repo
# changelogs have no hygiene gate").
#
# Auditing the CURRENT state (2026-09-04) turned up a live instance of the
# SAME fault the SDK gate's `tag` check exists for, not a hypothetical:
# `ui/` was tagged `v0.48.0` with `CHANGELOG.md` still reading `## Unreleased`
# at the top — the heading was simply never renamed, so the release the tag
# names has NO entry naming it in the file, one line above where it should be.
#
# ── Why one script, two modes, not a per-repo dialect ──────────────────────
#
# These five repos are separate git remotes with separate CI, so this file is
# necessarily COPIED into each — `issuer/scripts/`, `api/scripts/`,
# `ui/scripts/`, `website/scripts/`, `cli/scripts/` — there is no
# shared-checkout trick across repo boundaries the way sdk/'s single script
# covers three package managers from one clone. Keep the five copies
# byte-identical; a fix belongs in all five the same day.
#
# Unlike the SDK packages, none of these repos keeps a version field that
# tracks the release independently of the git tag (ui/package.json's
# `"version"` is NOT bumped per release — the tag alone is authoritative), so
# the subject here is the git tag being deployed, not a package manifest.
#
# THE VERSION MUST BE A WHOLE TOKEN, same boundary rule as the SDK script:
# `0.4.5` must not be satisfied by a heading for `0.4.50`, nor `10.3.6`, hence
# the non-[0-9.] boundary on both sides.
#
# THE VERSION MUST BE THE HEADING'S OWN SUBJECT, not merely mentioned in it.
# Until 2026-09-04 the match let the version appear ANYWHERE in the heading
# preceded by whitespace/`(`, which is exactly where a dependency bump or a
# version comparison sits: `## v0.10.0 — bump the web SDK to 0.9.0` reported
# `tag v0.9.0` present, and `ui/CHANGELOG.md` headings routinely name SDK
# versions in prose this way (`` `@realm-id/web-admin` `0.14.0` `` inside the
# v0.48.0 body, for one). The version must now be the token immediately after
# `## ` (optional leading `v`) — the heading's own subject — never a later
# mention.
#
# A HEADING EXISTING IS NOT AN ENTRY. Until 2026-09-04 `tag` mode only checked
# that a `## <version>` heading existed, not that any of the version's changes
# were written under it — so the fix for a missing entry ("rename `##
# Unreleased` to `## $version`") was indistinguishable from the bug it exists
# to catch: a bare rename manufactures a pass with zero content. `tag` mode now
# also requires at least one non-blank line between the heading and the next
# `## ` (or EOF).
#
# IT REFUSES TO INSPECT NOTHING. A missing CHANGELOG.md is a hard failure, not
# "this repo doesn't keep one" — a missing file reads as clean and isn't. Root
# `TODO.md`'s v0.106.0 promote-gate finding is the general case of this: a
# `paths-ignore`'d docs-only tag commit let `deploy.yml` read "no CI run for
# this SHA" as "docs-only, promote anyway", shipping a red `Tests` run
# (2026-08-28, `issuer/DECISIONS.md`). A gate that can be starved of input must
# not read that as a pass.
#
# Usage:
#   changelog-hygiene.sh order        # CHANGELOG.md reads in descending order;
#                                      # catches >1 `## Unreleased`, duplicate
#                                      # version headings, and unparseable ones.
#                                      # Run on every push — this is a property
#                                      # of the file at all times, not just at
#                                      # release (same reasoning as sdk/'s
#                                      # `order` mode).
#   changelog-hygiene.sh tag <vX.Y.Z>  # the tag about to be deployed has its
#                                      # own `## <version>` heading. Run once,
#                                      # at deploy time, gating promotion.
#
# Exit 0 = pass, 1 = a real hygiene failure, 2 = usage/parse/environment error.

set -euo pipefail

CHANGELOG="CHANGELOG.md"

say() {
  printf '%s\n' "$*"
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then printf '%s\n' "$*" >> "$GITHUB_STEP_SUMMARY"; fi
}

require_changelog() {
  if [ ! -f "$CHANGELOG" ]; then
    echo "::error::$CHANGELOG does not exist (run from the repo root). A missing changelog is a hard failure, not a clean run." >&2
    exit 2
  fi
}

# ver_gt <a> <b> — true when version a sorts strictly ABOVE version b.
ver_gt() {
  local a="$1" b="$2" i ac bc
  local -a A B
  IFS='.' read -r -a A <<< "$a"
  IFS='.' read -r -a B <<< "$b"
  for ((i = 0; i < ${#A[@]} || i < ${#B[@]}; i++)); do
    ac=${A[i]:-0}; bc=${B[i]:-0}
    if ((10#$ac > 10#$bc)); then return 0; fi
    if ((10#$ac < 10#$bc)); then return 1; fi
  done
  return 1
}

# fenced_headings <file> — `line:text` for every `^## ` heading OUTSIDE a
# ``` fenced code block, same shape as `grep -n`. Without this, a changelog
# entry that quotes a markdown example containing `## Unreleased` inside a
# fence is read as a second real heading and fails `order` on every push (no
# `paths-ignore` on that workflow, so this would turn main red over a legal
# changelog).
fenced_headings() {
  awk '
    /^```/ { infence = !infence; next }
    !infence && /^## / { print NR ":" $0 }
  ' "$1"
}

check_order() {
  require_changelog
  local prev="" prev_line="" line no text ver first=1 bad=0 headings=0
  say "## Changelog hygiene — descending order ($CHANGELOG)"
  say ""
  while IFS= read -r line; do
    no=${line%%:*}
    text=${line#*:}
    headings=$((headings + 1))
    if [[ "$text" =~ ^\#\#[[:space:]]+Unreleased ]]; then
      if [ "$first" = "1" ]; then first=0; continue; fi
      say "- ❌ **\`$CHANGELOG:$no\`** — a second \`## Unreleased\` heading. Merge it into the one at the top; never open a second."
      echo "::error file=$CHANGELOG,line=$no::a second ## Unreleased heading — merge into the top one" >&2
      bad=1
      continue
    fi
    first=0
    ver=$(printf '%s' "$text" | sed -nE 's/^## (v)?([0-9]+(\.[0-9]+)*).*/\2/p')
    if [ -z "$ver" ]; then
      say "- ❌ **\`$CHANGELOG:$no\`** — heading names no version: \`$text\`"
      echo "::error file=$CHANGELOG,line=$no::changelog heading names no version, so its order cannot be checked" >&2
      bad=1
      continue
    fi
    # A bare integer with no dot at all is admitted ONLY for the one known
    # pre-scheme heading (`## v1 (unreleased / dogfood-launch, 2026-04-22)`) —
    # pinned to its exact text, not to "any dot-less version", so a genuinely
    # malformed dot-less heading (a typo, a stray `## v2` someone adds later)
    # still fails the run instead of silently joining this exemption. Reported
    # either way so it is never silently invisible; comparing "1" against
    # "0.119.0" component-wise says 1 > 0, exactly backwards for content that
    # is, chronologically, the OLDEST entry in the file.
    if [[ "$ver" != *.* ]]; then
      if [ "$text" = "## v1 (unreleased / dogfood-launch, 2026-04-22)" ]; then
        say "- ℹ️  \`$CHANGELOG:$no\` — \`$text\` predates the \`vX.Y.Z\` scheme; not order-checked."
        continue
      fi
      say "- ❌ **\`$CHANGELOG:$no\`** — dot-less version \`$ver\` is not the known pre-scheme heading: \`$text\`"
      echo "::error file=$CHANGELOG,line=$no::dot-less changelog heading is not the recognised legacy exception — give it a real vX.Y.Z" >&2
      bad=1
      continue
    fi
    if [ -n "$prev" ]; then
      if ver_gt "$ver" "$prev"; then
        say "- ❌ **\`$CHANGELOG:$no\`** — \`$ver\` sits BELOW \`$prev\` (\`$CHANGELOG:$prev_line\`). A reader scanning down stops before it."
        echo "::error file=$CHANGELOG,line=$no::$ver is out of descending order (below $prev at line $prev_line)" >&2
        bad=1
      elif [ "$ver" = "$prev" ]; then
        say "- ❌ **\`$CHANGELOG:$no\`** — \`$ver\` appears twice (also \`$CHANGELOG:$prev_line\`)."
        echo "::error file=$CHANGELOG,line=$no::duplicate changelog heading for $ver" >&2
        bad=1
      fi
    fi
    prev="$ver"; prev_line="$no"
  done < <(fenced_headings "$CHANGELOG")

  if [ "$headings" = "0" ]; then
    echo "::error::changelog-hygiene order found 0 '^## ' headings in $CHANGELOG — the convention changed or the file is empty; this is a broken run, not clean" >&2
    exit 2
  fi
  if [ "$bad" = "1" ]; then
    say ""
    say "**More than one \`## Unreleased\`, an out-of-order heading, or one naming no"
    say "version.** Fix $CHANGELOG and re-run."
    exit 1
  fi
  say "- ✅ \`$CHANGELOG\` — in descending order, one \`## Unreleased\` at most."
  say ""
  say "Checked $headings heading(s)."
}

check_tag() {
  local tag="${1:-}"
  if [ -z "$tag" ]; then
    echo "::error::changelog-hygiene tag requires a version argument, e.g. 'changelog-hygiene.sh tag v0.119.0'" >&2
    exit 2
  fi
  require_changelog
  local version="${tag#v}" escaped
  escaped=$(printf '%s' "$version" | sed 's/\./\\./g')
  say "## Changelog hygiene — release entry for \`$tag\`"
  say ""

  # The version must be the heading's OWN SUBJECT — the token immediately
  # after "## " (optional "v") — never a later mention in the heading's prose.
  local heading_line
  heading_line=$(grep -nE "^## v?${escaped}([^0-9.]|\$)" "$CHANGELOG" | head -1 | cut -d: -f1 || true)

  if [ -z "$heading_line" ]; then
    say "- ❌ **\`$tag\`** — no \`## $version\` heading in \`$CHANGELOG\`."
    say ""
    say "**A release may not deploy without its own changelog entry.** If the top of"
    say "\`$CHANGELOG\` still reads \`## Unreleased\`, rename that heading to"
    say "\`## $version\` — but a rename alone will still fail the content check"
    say "below, because a heading naming this version is not the same thing as"
    say "this version's changes being described under it. Write what shipped."
    echo "::error::$tag has no entry in $CHANGELOG" >&2
    exit 1
  fi

  # A heading naming the version is not an entry — the version's OWN changes
  # must be written under it, not merely a heading with nothing beneath it
  # before the next one. Require >=1 non-blank line in that span.
  local next_line body_nonblank
  # NO `exit` in this awk, deliberately. `exit` closes the pipe while
  # fenced_headings is still scanning, the writer takes SIGPIPE, and `pipefail`
  # turns that into rc=141 -- which killed the v0.120.0 promotion. It is
  # platform-dependent: it passes on macOS and dies on Linux, so it cannot be
  # caught by running this script locally. Read the whole stream instead.
  next_line=$(fenced_headings "$CHANGELOG" | awk -F: -v start="$heading_line" '$1>start && !f {print $1; f=1}')
  if [ -z "$next_line" ]; then
    body_nonblank=$(awk -v start="$heading_line" 'NR>start && NF{c++} END{print c+0}' "$CHANGELOG")
  else
    body_nonblank=$(awk -v start="$heading_line" -v end="$next_line" 'NR>start && NR<end && NF{c++} END{print c+0}' "$CHANGELOG")
  fi

  if [ "${body_nonblank:-0}" = "0" ]; then
    say "- ❌ **\`$tag\`** — \`$CHANGELOG:$heading_line\` names \`$version\` but has NO"
    say "  content beneath it before the next heading. A heading that names a"
    say "  version is not an entry for it; describe what shipped."
    echo "::error file=$CHANGELOG,line=$heading_line::$tag's heading has no body — a heading is not an entry" >&2
    exit 1
  fi

  say "- ✅ \`$tag\` — entry present in \`$CHANGELOG\` (\`$CHANGELOG:$heading_line\`, $body_nonblank content line(s) before the next heading)."
  exit 0
}

case "${1:-}" in
  order) check_order ;;
  tag)   check_tag "${2:-}" ;;
  *)     echo "usage: $0 {order|tag <vX.Y.Z>}" >&2; exit 2 ;;
esac
