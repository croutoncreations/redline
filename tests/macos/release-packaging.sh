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

sparkle_private_key="${test_root}/sparkle-private-key"
sparkle_private_seed_hex="9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
sparkle_public_key_hex="d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a"
printf '%s' "${sparkle_private_seed_hex}" | xxd -r -p | base64 | tr -d '\n' >"${sparkle_private_key}"
sparkle_public_key="$(printf '%s' "${sparkle_public_key_hex}" | xxd -r -p | base64 | tr -d '\n')"

REDLINE_APP_OUTPUT_DIR="${test_root}/build" \
REDLINE_RELEASE_OUTPUT_DIR="${test_root}/release" \
REDLINE_VERSION="9.8.7" \
REDLINE_BUILD_NUMBER="42" \
REDLINE_SIGN_IDENTITY="-" \
REDLINE_ALLOW_UNNOTARIZED="1" \
REDLINE_SPARKLE_FEED_URL="https://updates.redline.example/appcast.xml" \
REDLINE_SPARKLE_DOWNLOAD_URL_PREFIX="https://updates.redline.example/releases/" \
REDLINE_SPARKLE_PUBLIC_KEY="${sparkle_public_key}" \
REDLINE_SPARKLE_PRIVATE_KEY_FILE="${sparkle_private_key}" \
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
test "$(plutil -extract SUFeedURL raw "${app_path}/Contents/Info.plist")" = "https://updates.redline.example/appcast.xml"
test "$(plutil -extract SUPublicEDKey raw "${app_path}/Contents/Info.plist")" = "${sparkle_public_key}"
test -s "${app_path}/Contents/Resources/AppIcon.icns"
codesign --verify --deep --strict "${app_path}"
signature_details="$(codesign -dvv "${app_path}" 2>&1)"
[[ "${signature_details}" == *"runtime"* ]]

printf 'not a base64 Sparkle key' >"${test_root}/invalid-sparkle-key"
if REDLINE_RELEASE_OUTPUT_DIR="${test_root}/release" \
  REDLINE_SPARKLE_FEED_URL="https://updates.redline.example/appcast.xml" \
  REDLINE_SPARKLE_PRIVATE_KEY_FILE="${test_root}/invalid-sparkle-key" \
  "${repository_root}/scripts/generate-sparkle-appcast.sh" \
    >"${test_root}/sparkle-key.stdout" 2>"${test_root}/sparkle-key.stderr"; then
  printf 'appcast generation unexpectedly accepted an invalid private key file\n' >&2
  exit 1
fi
grep -q 'must contain a base64-encoded 32-byte Ed25519 private seed' "${test_root}/sparkle-key.stderr"

hdiutil detach "${mount_path}" -quiet

REDLINE_APP_OUTPUT_DIR="${test_root}/build" \
REDLINE_RELEASE_OUTPUT_DIR="${test_root}/release" \
REDLINE_VERSION="9.8.8" \
REDLINE_BUILD_NUMBER="43" \
REDLINE_SIGN_IDENTITY="-" \
REDLINE_ALLOW_UNNOTARIZED="1" \
REDLINE_GENERATE_APPCAST="1" \
REDLINE_SPARKLE_FEED_URL="https://updates.redline.example/appcast.xml" \
REDLINE_SPARKLE_DOWNLOAD_URL_PREFIX="https://updates.redline.example/releases/" \
REDLINE_SPARKLE_PUBLIC_KEY="${sparkle_public_key}" \
REDLINE_SPARKLE_PRIVATE_KEY_FILE="${sparkle_private_key}" \
  "${repository_root}/scripts/package-macos-release.sh"

new_dmg_path="${test_root}/release/Redline-9.8.8-arm64.dmg"
delta_path="${test_root}/release/Redline43-42.delta"
appcast_path="${test_root}/release/appcast.xml"
test -s "${new_dmg_path}"
test -s "${delta_path}"
test -s "${appcast_path}"
grep -q '<sparkle:version>43</sparkle:version>' "${appcast_path}"
grep -q '<sparkle:version>42</sparkle:version>' "${appcast_path}"
grep -q 'sparkle:deltaFrom="42"' "${appcast_path}"

archive_signature="$(xmllint --xpath "string(//*[local-name()='item'][*[local-name()='version']='43']/*[local-name()='enclosure'][not(@*[local-name()='deltaFrom'])]/@*[local-name()='edSignature'])" "${appcast_path}")"
delta_signature="$(xmllint --xpath "string(//*[local-name()='item'][*[local-name()='version']='43']/*[local-name()='deltas']/*[local-name()='enclosure']/@*[local-name()='edSignature'])" "${appcast_path}")"
"${repository_root}/macos/.build/artifacts/sparkle/Sparkle/bin/sign_update" \
  --verify --ed-key-file "${sparkle_private_key}" "${new_dmg_path}" "${archive_signature}"
"${repository_root}/macos/.build/artifacts/sparkle/Sparkle/bin/sign_update" \
  --verify --ed-key-file "${sparkle_private_key}" "${delta_path}" "${delta_signature}"

printf 'macOS two-version release packaging and Sparkle delta test passed: %s\n' "${appcast_path}"
