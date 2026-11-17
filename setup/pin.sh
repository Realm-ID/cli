#!/usr/bin/env bash
# pin.sh <checksums.txt> <version> <pins-dir>
#
# Writes <pins-dir>/<version>.sha256 holding the linux archives' sha256 lines,
# taken from the checksums file the release build produced (goreleaser's dist/,
# never a file downloaded from the published release). An existing pin is never
# overwritten: a pin is written once, by the release that builds that version.
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: pin.sh <checksums.txt> <version> <pins-dir>" >&2
  exit 2
fi
src="$1"
ver="${2#v}"
out="$3/${ver}.sha256"

if [ -e "${out}" ]; then
  echo "refusing to overwrite existing pin ${out}" >&2
  exit 1
fi

mkdir -p "$3"
grep -E "^[0-9a-f]{64}  realm-id_${ver}_linux_(amd64|arm64)\.tar\.gz$" "${src}" > "${out}.tmp" || true
if [ "$(wc -l < "${out}.tmp" | tr -d ' ')" != 2 ]; then
  rm -f "${out}.tmp"
  echo "expected both linux archives for ${ver} in ${src}" >&2
  exit 1
fi
mv "${out}.tmp" "${out}"
echo "wrote ${out}"
