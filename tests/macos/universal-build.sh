#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/redline-universal-test.XXXXXX")"
trap 'rm -rf "${test_root}"' EXIT

REDLINE_APP_OUTPUT_DIR="${test_root}" \
REDLINE_BUILD_ARCH="universal" \
REDLINE_SIGN_IDENTITY="-" \
  "${repository_root}/scripts/build-macos-app.sh"

app_path="${test_root}/Redline.app"
for executable in \
  "${app_path}/Contents/MacOS/RedlineMenuBar" \
  "${app_path}/Contents/Resources/bin/redline"; do
  slices="$(lipo -archs "${executable}")"
  [[ " ${slices} " == *" arm64 "* ]]
  [[ " ${slices} " == *" x86_64 "* ]]
done

while IFS= read -r executable; do
  slices="$(lipo -archs "${executable}")"
  [[ " ${slices} " == *" arm64 "* ]]
  [[ " ${slices} " == *" x86_64 "* ]]
done < <(find "${app_path}" -type f -print0 | xargs -0 file | sed -n 's/: Mach-O.*$//p')

codesign --verify --deep --strict "${app_path}"
service="${app_path}/Contents/Resources/bin/redline"
for architecture in arm64 x86_64; do
  output="$(/usr/bin/arch -"${architecture}" "${service}" unsupported-command 2>&1 || true)"
  [[ "${output}" == *'unknown command "unsupported-command"'* ]]
done
printf 'Universal macOS build passed architecture and signing checks.\n'
