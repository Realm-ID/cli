#!/usr/bin/env bash
# verify.sh <pins-dir> <version> <asset-name> <file>
#
# Succeeds only when <file>'s sha256 equals the entry for <asset-name> in
# <pins-dir>/<version>.sha256, a file committed in this repository. The pin is
# read from the checked-out action, never downloaded alongside the artifact.
set -euo pipefail

if [ "$#" -ne 4 ]; then
  echo "usage: verify.sh <pins-dir> <version> <asset-name> <file>" >&2
  exit 2
fi
pins="$1/$2.sha256"
name="$3"
file="$4"

if [ ! -s "${pins}" ]; then
  echo "::error::no committed pin for realm-id ${2} (${pins}); use a setup action revision that includes that release" >&2
  exit 1
fi

# Exactly one entry, matched on the whole asset name.
expected="$(awk -v n="${name}" '$2 == n { print $1 }' "${pins}")"
if [ -z "${expected}" ] || [ "$(printf '%s\n' "${expected}" | wc -l | tr -d ' ')" != 1 ]; then
  echo "::error::${name} has no single entry in the committed pin ${pins}" >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "${file}" | cut -d' ' -f1)"
else
  actual="$(shasum -a 256 "${file}" | cut -d' ' -f1)"
fi

if [ "${actual}" != "${expected}" ]; then
  echo "::error::sha256 of ${name} does not match the committed pin" >&2
  exit 1
fi
echo "${name}: sha256 matches the committed pin"
