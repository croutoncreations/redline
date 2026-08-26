#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/redline-arm64-test.XXXXXX")"
trap 'rm -rf "${test_root}"' EXIT

app_path="${REDLINE_ARM64_APP_PATH:-${test_root}/Redline.app}"
if [[ "${REDLINE_ARM64_SKIP_BUILD:-0}" != "1" ]]; then
  REDLINE_APP_OUTPUT_DIR="$(dirname "${app_path}")" \
  REDLINE_BUILD_ARCH="arm64" \
  REDLINE_SIGN_IDENTITY="-" \
    "${repository_root}/scripts/build-macos-app.sh"
fi

if [[ ! -d "${app_path}" ]]; then
  printf 'ARM64 app is missing: %s\n' "${app_path}" >&2
  exit 1
fi
while IFS= read -r executable; do
  slices="$(lipo -archs "${executable}")"
  if [[ "${slices}" != "arm64" ]]; then
    printf 'Unexpected slices %s in %s\n' "${slices}" "${executable}" >&2
    exit 1
  fi
done < <(find "${app_path}" -type f -perm -111 -print0 | xargs -0 file | sed -n 's/: Mach-O.*$//p')

codesign --verify --deep --strict "${app_path}"
printf 'ARM64 macOS build contains no Intel executable slices.\n'
