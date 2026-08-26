#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version="${REDLINE_VERSION:-0.1.0}"
sign_identity="${REDLINE_SIGN_IDENTITY:--}"
notary_profile="${REDLINE_NOTARY_PROFILE:-}"
allow_unnotarized="${REDLINE_ALLOW_UNNOTARIZED:-0}"
sparkle_feed_url="${REDLINE_SPARKLE_FEED_URL:-}"
sparkle_public_key="${REDLINE_SPARKLE_PUBLIC_KEY:-}"
generate_appcast="${REDLINE_GENERATE_APPCAST:-0}"
app_output_root="${REDLINE_APP_OUTPUT_DIR:-${repository_root}/dist}"
release_output_root="${REDLINE_RELEASE_OUTPUT_DIR:-${repository_root}/dist/releases}"

if [[ ! "${version}" =~ ^[0-9]+(\.[0-9]+){1,2}$ ]]; then
  printf 'REDLINE_VERSION must contain two or three numeric components (for example 1.2.0).\n' >&2
  exit 1
fi
if [[ "${allow_unnotarized}" != "1" && "${sign_identity}" != "Developer ID Application:"* ]]; then
  printf 'A Developer ID Application identity is required. Set REDLINE_ALLOW_UNNOTARIZED=1 only for local packaging tests.\n' >&2
  exit 1
fi
if [[ -z "${notary_profile}" && "${allow_unnotarized}" != "1" ]]; then
  printf 'REDLINE_NOTARY_PROFILE is required for a distributable release.\n' >&2
  exit 1
fi
if [[ "${allow_unnotarized}" != "1" && ( -z "${sparkle_feed_url}" || -z "${sparkle_public_key}" ) ]]; then
  printf 'REDLINE_SPARKLE_FEED_URL and REDLINE_SPARKLE_PUBLIC_KEY are required for a distributable release.\n' >&2
  exit 1
fi

export REDLINE_BUILD_ARCH="${REDLINE_BUILD_ARCH:-universal}"
export REDLINE_SIGN_IDENTITY="${sign_identity}"
"${repository_root}/scripts/build-macos-app.sh"

app_path="${app_output_root}/Redline.app"
build_slices="$(lipo -archs "${app_path}/Contents/MacOS/RedlineMenuBar")"
if [[ " ${build_slices} " == *" arm64 "* && " ${build_slices} " == *" x86_64 "* ]]; then
  build_arch="universal"
else
  build_arch="${build_slices}"
fi
release_name="Redline-${version}-${build_arch}"
dmg_path="${release_output_root}/${release_name}.dmg"
temporary_root="$(mktemp -d "${TMPDIR:-/tmp}/redline-macos-package.XXXXXX")"
trap 'rm -rf "${temporary_root}"' EXIT
mkdir -p "${release_output_root}"

notarize() {
  local submission_path="$1"
  local label="$2"
  local result_path="${release_output_root}/${release_name}-${label}-notary-result.plist"
  local log_path="${release_output_root}/${release_name}-${label}-notary-log.json"
  xcrun notarytool submit "${submission_path}" --keychain-profile "${notary_profile}" --wait --output-format plist > "${result_path}"
  local status
  local submission_id
  status="$(plutil -extract status raw "${result_path}")"
  submission_id="$(plutil -extract id raw "${result_path}")"
  xcrun notarytool log "${submission_id}" --keychain-profile "${notary_profile}" "${log_path}"
  if [[ "${status}" != "Accepted" ]]; then
    printf 'Notarization failed for %s; see %s\n' "${label}" "${log_path}" >&2
    exit 1
  fi
}

if [[ -n "${notary_profile}" ]]; then
  app_zip="${temporary_root}/${release_name}.zip"
  ditto -c -k --keepParent "${app_path}" "${app_zip}"
  notarize "${app_zip}" app
  xcrun stapler staple "${app_path}"
  xcrun stapler validate "${app_path}"
fi

staging_path="${temporary_root}/dmg"
mkdir -p "${staging_path}"
ditto "${app_path}" "${staging_path}/Redline.app"
ln -s /Applications "${staging_path}/Applications"
rm -f "${dmg_path}"
hdiutil create -volname Redline -srcfolder "${staging_path}" -ov -format UDZO "${dmg_path}"

if [[ "${sign_identity}" != "-" ]]; then
  codesign --force --sign "${sign_identity}" --timestamp "${dmg_path}"
fi
hdiutil verify "${dmg_path}" >/dev/null

if [[ -n "${notary_profile}" ]]; then
  notarize "${dmg_path}" dmg
  xcrun stapler staple "${dmg_path}"
  xcrun stapler validate "${dmg_path}"
  spctl --assess --type open --context context:primary-signature --verbose=2 "${dmg_path}"
  printf 'Built notarized release %s\n' "${dmg_path}"
else
  printf 'Built LOCAL-ONLY unnotarized package %s\n' "${dmg_path}"
fi

(cd "${release_output_root}" && shasum -a 256 "$(basename "${dmg_path}")" > "$(basename "${dmg_path}").sha256")
printf 'SHA-256: %s.sha256\n' "${dmg_path}"

if [[ "${allow_unnotarized}" != "1" || "${generate_appcast}" == "1" ]]; then
  "${repository_root}/scripts/generate-sparkle-appcast.sh"
fi
