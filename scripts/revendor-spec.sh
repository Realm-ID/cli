#!/usr/bin/env bash
# Re-vendors the issuer's OpenAPI contract into cmd/realm-id/openapi.yaml from
# a RELEASE TAG of Realm-ID/issuer, and rewrites cmd/realm-id/ISSUER_CONTRACT
# with the provenance the integrity gate checks.
#
# WHY THIS MATTERS MORE HERE THAN ANYWHERE ELSE. This CLI has no SDK
# dependency: the embedded spec IS the source of every command, flag and
# argument — the tree is generated from it at startup (ADR-062 §1). A wrong
# vendor does not produce a wrong document, it produces a wrong PROGRAM.
#
# WHY TAG-ONLY. This replaced `//go:generate cp ../../../issuer/docs/swagger.yaml
# openapi.yaml`, a copy from the sibling WORKING TREE. That could vendor an
# unreleased, mid-edit or dirty spec — a spec describing an issuer nobody is
# running — and nothing downstream would notice. A branch head, `HEAD`, or a
# path is refused here for the same reason.
#
# WHY THE TAG, NOT `info.version`. The spec's own version does not uniquely
# identify a deployment: issuer v0.121.0 and v0.121.1 both serve 0.46.0. The
# tag is the identity; info.version is recorded alongside it because that is
# what a reader (and this repo's DECISIONS.md, which tracks re-vendors by it)
# looks for.
#
# ALIGNMENT WITH THE BFF. In session mode — the default — every generated
# command is sent to `bffURL() + "/api"` (see cmd/realm-id/commands.go
# resolveCredential), i.e. through Realm-ID/api's passthrough, which strips the
# prefix and forwards the issuer path verbatim. So this CLI and that BFF must
# be pinned to the SAME issuer release, or the CLI offers commands the BFF's
# own pinned contract does not describe. Neither repo can see the other, so the
# UMBRELLA enforces it: scripts/issuer-pin-parity.py in Realm-ID/project.
#
# This script is a deliberate near-copy of api/scripts/revendor-spec.sh. The
# two repos have separate CI checkouts and separate release trains, so neither
# can call the other's; the umbrella pin-parity check is what keeps the two
# copies from drifting into disagreement about what is pinned.
#
# Usage:
#   revendor-spec.sh <vX.Y.Z>   # re-vendor from that issuer release tag
#   revendor-spec.sh selftest   # exercises the tag-only refusal; see Makefile
#
#   1. `git -C ../issuer show <tag>:docs/swagger.yaml` — offline, no auth,
#      when a sibling checkout has the tag fetched.
#   2. `gh api repos/Realm-ID/issuer/contents/docs/swagger.yaml?ref=<tag>` —
#      needs a credential the DEVELOPER already has. CI never runs this: CI
#      reads the committed copy, which is why the gate always runs.
#
# Exit 2 = usage/environment error (bad args, no source available), distinct
# from 1 = a real refusal.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SPEC_OUT="$repo_root/cmd/realm-id/openapi.yaml"
CONTRACT_OUT="$repo_root/cmd/realm-id/ISSUER_CONTRACT"
ISSUER_DIR="${ISSUER_DIR:-$repo_root/../issuer}"
ISSUER_REPO="${ISSUER_REPO:-Realm-ID/issuer}"
SPEC_PATH="docs/swagger.yaml"

# A release tag and nothing else. Not `main`, not `HEAD`, not a branch, not a
# path. This regex is the whole constraint — everything else in this script is
# plumbing.
is_release_tag() {
  [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]
}

sha256_of() {
  if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}';
  else sha256sum "$1" | awk '{print $1}'; fi
}

fetch_spec() {
  local tag="$1" out="$2"
  # Source 1: a sibling checkout that actually HAS the tag. `git show <tag>:`
  # reads the tagged blob, never the worktree, so a dirty tree cannot leak in.
  if [ -d "$ISSUER_DIR/.git" ] && git -C "$ISSUER_DIR" rev-parse -q --verify "refs/tags/$tag" >/dev/null 2>&1; then
    if git -C "$ISSUER_DIR" show "$tag:$SPEC_PATH" > "$out" 2>/dev/null; then
      echo "source: git -C $ISSUER_DIR show $tag:$SPEC_PATH" >&2
      return 0
    fi
  fi
  # Source 2: GitHub, pinned to the same ref. Both repos are private, so this
  # needs the developer's own credential.
  if command -v gh >/dev/null 2>&1; then
    if gh api "repos/$ISSUER_REPO/contents/$SPEC_PATH?ref=$tag" \
         -H 'Accept: application/vnd.github.raw' > "$out" 2>/dev/null; then
      echo "source: gh api repos/$ISSUER_REPO/contents/$SPEC_PATH?ref=$tag" >&2
      return 0
    fi
  fi
  return 1
}

revendor() {
  local tag="$1"
  if ! is_release_tag "$tag"; then
    echo "::error::revendor-spec: '$tag' is not an issuer RELEASE TAG (vX.Y.Z)." >&2
    echo "  A branch, HEAD, or a working-tree path describes an issuer nobody is running." >&2
    echo "  Cut/pick the release tag first, then re-run: $0 vX.Y.Z" >&2
    return 1
  fi

  local tmp
  tmp="$(mktemp)"
  if ! fetch_spec "$tag" "$tmp"; then
    rm -f "$tmp"
    echo "::error::revendor-spec: could not read $SPEC_PATH at $tag." >&2
    echo "  Tried: a sibling checkout at $ISSUER_DIR with that tag fetched," >&2
    echo "  then 'gh api' against $ISSUER_REPO (private — needs your credential)." >&2
    echo "  Fix: 'git -C $ISSUER_DIR fetch --tags', or authenticate 'gh auth login'." >&2
    exit 2
  fi
  if [ ! -s "$tmp" ]; then
    rm -f "$tmp"
    echo "::error::revendor-spec: $SPEC_PATH at $tag came back EMPTY — refusing to vendor it." >&2
    exit 2
  fi

  local info_version
  info_version="$(awk '/^info:/{f=1;next} f&&/^  version:/{print $2;exit} f&&/^[a-z]/{exit}' "$tmp")"
  if [ -z "$info_version" ]; then
    rm -f "$tmp"
    echo "::error::revendor-spec: could not read info.version out of the fetched spec — refusing to vendor it." >&2
    exit 2
  fi

  # The re-vendor is the moment to SEE what moved in the contract. cli's
  # DECISIONS.md tracks eight re-vendors and every one of them wanted this.
  echo ""
  echo "== contract diff: $(cat "$CONTRACT_OUT" 2>/dev/null | awk -F= '/^issuer_tag=/{print $2}') -> $tag =="
  if [ -f "$SPEC_OUT" ]; then
    if diff -q "$SPEC_OUT" "$tmp" >/dev/null 2>&1; then
      echo "  (byte-identical — nothing moved)"
    else
      # `diff` exits 1 when files differ, which is the ONLY branch we reach here
      # — under `set -o pipefail` that aborts the whole script. Capture the diff
      # once, tolerate its exit status explicitly, then read it twice.
      local d
      d="$(diff -u "$SPEC_OUT" "$tmp" || true)"
      printf '%s\n' "$d" | awk '
        /^\+\+\+|^---/ {next}
        /^[+-][[:space:]]*\/[^ ]*:/ {print "  PATH  " $0; next}
        # 4-space keys under components.schemas. The same indent carries the
        # verbs of a path item, so drop those by name - otherwise every changed
        # endpoint reports a bare "get:" / "post:" and buries the real names.
        /^[+-][[:space:]]{4}(get|put|post|patch|delete|options|head|parameters|servers|summary|description):$/ {next}
        /^[+-][[:space:]]{4}[A-Za-z0-9]+:$/ {print "  SCHEMA" $0; next}
      ' | sort -u | head -60
      echo "  --- line counts ---"
      printf '%s\n' "$d" | awk '/^\+[^+]/{a++} /^-[^-]/{d++} END{printf "  +%d / -%d lines\n", a+0, d+0}'
    fi
  else
    echo "  (first vendor — no previous copy to diff)"
  fi
  echo ""

  mv "$tmp" "$SPEC_OUT"
  local sha
  sha="$(sha256_of "$SPEC_OUT")"

  cat > "$CONTRACT_OUT" <<EOF
# Which issuer release this CLI's command tree is generated from.
#
# REGENERATED BY scripts/revendor-spec.sh — do not hand-edit. The integrity
# gate (cmd/realm-id/spec_contract_test.go) re-hashes the embedded spec and
# fails if these two files disagree, so a hand-edit of either is caught, not
# absorbed. The umbrella's scripts/issuer-pin-parity.py additionally checks
# that issuer_tag here MATCHES the BFF's pin in api/contract/ISSUER_CONTRACT.
#
# issuer_tag is the IDENTITY (a deployment); spec_info_version is what the spec
# calls itself and does NOT uniquely name a release.
issuer_tag=$tag
issuer_repo=$ISSUER_REPO
spec_path=$SPEC_PATH
spec_info_version=$info_version
spec_sha256=$sha
vendored_at=$(date -u +%Y-%m-%d)
EOF

  echo "vendored $ISSUER_REPO@$tag ($SPEC_PATH, info.version $info_version)"
  echo "  -> ${SPEC_OUT#"$repo_root"/}  sha256 $sha"
  echo "  -> ${CONTRACT_OUT#"$repo_root"/}"
  echo ""
  echo "Next:"
  echo "  1. go test ./cmd/realm-id/ -run TestVendoredSpec -count=1   (integrity + the tree still builds)"
  echo "  2. diff the COMMAND TREE, not just the spec — a re-vendor changes what this"
  echo "     binary can do: realm-id --json help > after.json and compare."
  echo "  3. from the workspace root: python3 scripts/issuer-pin-parity.py — the CLI"
  echo "     and the BFF must be pinned to the SAME issuer release."
}

selftest() {
  local rc
  # The load-bearing property: anything that is not vX.Y.Z is refused BEFORE
  # any fetch happens. A green here is what makes the vendored copy mean
  # "a released issuer".
  for bad in main HEAD develop v1.2 v1.2.3-rc1 ../issuer/docs/swagger.yaml "" 1.2.3 v1.2.3.4; do
    rc=0
    is_release_tag "$bad" || rc=$?
    if [ "$rc" = "0" ]; then
      echo "self-test FAILED: '$bad' was accepted as a release tag" >&2
      exit 1
    fi
  done
  for good in v0.121.1 v1.0.0 v10.20.30; do
    rc=0
    is_release_tag "$good" || rc=$?
    if [ "$rc" != "0" ]; then
      echo "self-test FAILED: '$good' was refused as a release tag" >&2
      exit 1
    fi
  done
  # And the refusal must be a real exit 1 through the public entry point, not
  # just a helper returning false.
  rc=0
  ( revendor main >/dev/null 2>&1 ) || rc=$?
  if [ "$rc" != "1" ]; then
    echo "self-test FAILED: 'revendor main' should exit 1 (refusal), got $rc" >&2
    exit 1
  fi
  echo "revendor-spec self-test: PASS (8 non-tags refused, 3 tags accepted, 'main' exits 1)"
}

case "${1:-}" in
  selftest) selftest ;;
  "")       echo "usage: $0 {<vX.Y.Z>|selftest}" >&2; exit 2 ;;
  *)        revendor "$1" ;;
esac
