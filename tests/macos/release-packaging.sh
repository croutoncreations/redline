#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/redline-release-test.XXXXXX")"
mount_path="${test_root}/mounted"
cleanup() {
  hdiutil detach "${mount_path}" -quiet 2>/dev/null || true
  rm -rf "${test_root}"
}
trap cleanup EXIT

if REDLINE_SIGN_IDENTITY="-" REDLINE_ALLOW_UNNOTARIZED="0" \
  "${repository_root}/scripts/package-macos-release.sh" >"${test_root}/guard.stdout" 2>"${test_root}/guard.stderr"; then
  printf 'release packaging unexpectedly accepted an ad-hoc identity\n' >&2
  exit 1
fi
grep -q 'Developer ID Application identity is required' "${test_root}/guard.stderr"
if REDLINE_SIGN_IDENTITY="Apple Development: Example (TEAMID)" REDLINE_NOTARY_PROFILE="test" \
  "${repository_root}/scripts/package-macos-release.sh" >"${test_root}/identity.stdout" 2>"${test_root}/identity.stderr"; then
  printf 'release packaging unexpectedly accepted an Apple Development identity\n' >&2
  exit 1
fi
grep -q 'Developer ID Application identity is required' "${test_root}/identity.stderr"
if REDLINE_APP_OUTPUT_DIR="${test_root}/invalid-build" REDLINE_BUILD_NUMBER="0" \
  "${repository_root}/scripts/build-macos-app.sh" >"${test_root}/version.stdout" 2>"${test_root}/version.stderr"; then
  printf 'app build unexpectedly accepted build number zero\n' >&2
  exit 1
fi
grep -q 'REDLINE_BUILD_NUMBER must be a positive integer' "${test_root}/version.stderr"

REDLINE_APP_OUTPUT_DIR="${test_root}/build" \
REDLINE_RELEASE_OUTPUT_DIR="${test_root}/release" \
REDLINE_VERSION="9.8.7" \
REDLINE_BUILD_NUMBER="42" \
REDLINE_SIGN_IDENTITY="-" \
REDLINE_ALLOW_UNNOTARIZED="1" \
  "${repository_root}/scripts/package-macos-release.sh"

dmg_path="${test_root}/release/Redline-9.8.7-arm64.dmg"
test -s "${dmg_path}"
test -s "${dmg_path}.sha256"
(cd "$(dirname "${dmg_path}")" && shasum -a 256 -c "$(basename "${dmg_path}").sha256")
hdiutil verify "${dmg_path}" -quiet
mkdir -p "${mount_path}"
hdiutil attach "${dmg_path}" -nobrowse -readonly -mountpoint "${mount_path}" -quiet

app_path="${mount_path}/Redline.app"
test -d "${app_path}"
test -L "${mount_path}/Applications"
test "$(plutil -extract CFBundleShortVersionString raw "${app_path}/Contents/Info.plist")" = "9.8.7"
test "$(plutil -extract CFBundleVersion raw "${app_path}/Contents/Info.plist")" = "42"
test "$(plutil -extract CFBundleIconFile raw "${app_path}/Contents/Info.plist")" = "AppIcon"
test -s "${app_path}/Contents/Resources/AppIcon.icns"
codesign --verify --deep --strict "${app_path}"
signature_details="$(codesign -dvv "${app_path}" 2>&1)"
[[ "${signature_details}" == *"runtime"* ]]

printf 'macOS release packaging test passed: %s\n' "${dmg_path}"
