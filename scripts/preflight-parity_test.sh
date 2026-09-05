#!/usr/bin/env sh
# Tests for preflight-parity.sh. Runs BEFORE it in `make check` — a broken
# checker must fail as a broken checker, never silently pass or manufacture a
# false finding (see .scratch/preflight/SPEC.md § Checker-tests rule, and the
# todo-ranking-hygiene.py precedent it cites).
set -eu

script_dir=$(cd "$(dirname "$0")" && pwd)
repo_root=$(cd "$script_dir/.." && pwd)
checker="$script_dir/preflight-parity.sh"

fixtures="$repo_root/.scratch/preflight-parity-test.$$"
trap 'rm -rf "$fixtures"' EXIT
mkdir -p "$fixtures/.github/workflows"

fail=0
ok() { echo "ok - $1"; }
bad() { echo "not ok - $1"; fail=1; }

# 1. Against the REAL repo, the checker must pass (this is the live parity
#    check, not a fixture — if it fails here, check/self-test must also fail).
if "$checker" "$repo_root" >/dev/null 2>&1; then
  ok "passes against the real repo's workflow files"
else
  bad "FAILS against the real repo's workflow files (Makefile/CI have drifted, or the checker itself is broken)"
fi

# 2. A fixture with every expected string present must pass.
cat > "$fixtures/.github/workflows/tests.yml" <<'EOF'
run: gofmt -l cmd
run: go build ./...
run: go vet ./...
run: go test -race ./...
EOF
cat > "$fixtures/.github/workflows/changelog-hygiene.yml" <<'EOF'
run: ./scripts/changelog-hygiene.sh order
EOF
cat > "$fixtures/.github/workflows/release.yml" <<'EOF'
run: ./scripts/changelog-hygiene.sh tag "$GITHUB_REF_NAME"
EOF
if "$checker" "$fixtures" >/dev/null 2>&1; then
  ok "passes a fixture that carries every expected command string"
else
  bad "fails a fixture that carries every expected command string (false positive)"
fi

# 3. Drift: drop one command from tests.yml — must fail, and must exit non-zero.
cat > "$fixtures/.github/workflows/tests.yml" <<'EOF'
run: gofmt -l cmd
run: go build ./...
run: go vet ./...
EOF
if "$checker" "$fixtures" >/dev/null 2>&1; then
  bad "did NOT catch a dropped 'go test -race ./...' (drift went undetected)"
else
  ok "catches a dropped command (go test -race ./...)"
fi

# 4. A missing workflow file entirely must fail, not read as clean.
rm -f "$fixtures/.github/workflows/release.yml"
if "$checker" "$fixtures" >/dev/null 2>&1; then
  bad "did NOT fail when a workflow file is entirely absent"
else
  ok "fails when a workflow file is entirely absent"
fi

if [ "$fail" != "0" ]; then
  echo "preflight-parity_test.sh: FAILED" >&2
  exit 1
fi
echo "preflight-parity_test.sh: all cases passed"
