#!/usr/bin/env sh
# Asserts the local `make check` gates run the SAME command strings CI does.
#
# `check`'s gofmt/build/vet/test/changelog-order steps are minimal re-typings
# of what .github/workflows/tests.yml and changelog-hygiene.yml already run —
# not calls into a shared script, because those steps are one-liners inline in
# the YAML. A drift between the two copies would make the LOCAL gate silently
# weaker than CI's, which is worse than no local gate at all (see
# .scratch/preflight/SPEC.md § Anti-duplication rule). This script is the
# tripwire: it fails loudly the moment either side changes without the other.
#
# Usage: preflight-parity.sh [root-dir]   (root-dir defaults to '.', tests
# pass a fixture directory so this can be exercised without touching the real
# workflow files.)
set -eu

root="${1:-.}"
fail=0

check_present() {
  file="$root/$1"
  needle="$2"
  if [ ! -f "$file" ]; then
    echo "::error::preflight-parity: $file does not exist" >&2
    fail=1
    return
  fi
  if ! grep -qF -- "$needle" "$file"; then
    echo "::error::preflight-parity: '$needle' not found in $file — Makefile's check target and CI have drifted" >&2
    fail=1
  fi
}

check_present ".github/workflows/tests.yml"             "gofmt -l cmd"
check_present ".github/workflows/tests.yml"              "go build ./..."
check_present ".github/workflows/tests.yml"              "go vet ./..."
check_present ".github/workflows/tests.yml"              "go test -race ./..."
check_present ".github/workflows/changelog-hygiene.yml"  "./scripts/changelog-hygiene.sh order"
check_present ".github/workflows/release.yml"            "./scripts/changelog-hygiene.sh tag"

if [ "$fail" != "0" ]; then
  exit 1
fi
echo "preflight-parity: Makefile's check target still matches the workflow YAML"
