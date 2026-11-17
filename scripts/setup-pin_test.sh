#!/usr/bin/env sh
# Tests for setup/verify.sh and setup/pin.sh: the setup action installs only a
# tarball whose sha256 matches a pin committed under setup/pins/.
set -eu

script_dir=$(cd "$(dirname "$0")" && pwd)
repo_root=$(cd "$script_dir/.." && pwd)
verify="$repo_root/setup/verify.sh"
pin="$repo_root/setup/pin.sh"

work="$repo_root/.scratch/setup-pin-test.$$"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/pins" "$work/dl"

fail=0
ok() { echo "ok - $1"; }
bad() { echo "not ok - $1"; fail=1; }

sum() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1; }

name=realm-id_9.9.9_linux_amd64.tar.gz
other=realm-id_9.9.9_linux_arm64.tar.gz
printf 'genuine amd64' > "$work/dl/$name"
printf 'genuine arm64' > "$work/dl/$other"
printf 'darwin' > "$work/dl/realm-id_9.9.9_darwin_amd64.tar.gz"

# pin.sh keeps only the linux archives, from the build's own checksums.
{
  echo "$(sum "$work/dl/$name")  $name"
  echo "$(sum "$work/dl/$other")  $other"
  echo "$(sum "$work/dl/realm-id_9.9.9_darwin_amd64.tar.gz")  realm-id_9.9.9_darwin_amd64.tar.gz"
} > "$work/checksums.txt"
"$pin" "$work/checksums.txt" 9.9.9 "$work/pins" >/dev/null 2>&1 || true
if [ -f "$work/pins/9.9.9.sha256" ] && [ "$(wc -l < "$work/pins/9.9.9.sha256" | tr -d ' ')" = 2 ] \
  && ! grep -q darwin "$work/pins/9.9.9.sha256"; then
  ok "pin.sh writes the linux archives only"
else bad "pin.sh output wrong"; fi

# 1. The genuine tarball verifies.
if "$verify" "$work/pins" 9.9.9 "$name" "$work/dl/$name" >/dev/null 2>&1; then
  ok "genuine tarball verifies"
else bad "genuine tarball rejected"; fi

# 2. A tampered tarball fails.
printf 'tampered' > "$work/dl/$name"
if "$verify" "$work/pins" 9.9.9 "$name" "$work/dl/$name" >/dev/null 2>&1; then
  bad "tampered tarball ACCEPTED"
else ok "tampered tarball fails"; fi

# 3. A version with no committed pin fails.
if "$verify" "$work/pins" 8.8.8 "$name" "$work/dl/$name" >/dev/null 2>&1; then
  bad "unpinned version ACCEPTED"
else ok "version without a pin fails"; fi

# 4. A pin file that lacks the asset fails.
echo "$(sum "$work/dl/$other")  $other" > "$work/pins/7.7.7.sha256"
if "$verify" "$work/pins" 7.7.7 "$name" "$work/dl/$name" >/dev/null 2>&1; then
  bad "asset missing from pin ACCEPTED"
else ok "asset missing from the pin fails"; fi

# 5. An empty pin file fails.
: > "$work/pins/6.6.6.sha256"
if "$verify" "$work/pins" 6.6.6 "$name" "$work/dl/$name" >/dev/null 2>&1; then
  bad "empty pin ACCEPTED"
else ok "empty pin fails"; fi

# 6. pin.sh refuses a checksums file with no linux archive, and an existing pin.
: > "$work/empty.txt"
if "$pin" "$work/empty.txt" 5.5.5 "$work/pins" >/dev/null 2>&1; then bad "pin.sh accepted empty input"; else ok "pin.sh refuses input with no linux archive"; fi
if "$pin" "$work/checksums.txt" 9.9.9 "$work/pins" >/dev/null 2>&1; then bad "pin.sh overwrote an existing pin"; else ok "pin.sh never overwrites a pin"; fi

# 7. The action uses the committed pins and fetches no checksum file.
if grep -q 'verify.sh' "$repo_root/setup/action.yml" && ! grep -q 'checksums.txt' "$repo_root/setup/action.yml"; then
  ok "action.yml verifies against setup/pins and fetches no checksums.txt"
else bad "action.yml still reads a release checksum file"; fi

[ "$fail" = 0 ]
