#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output_root="${REDLINE_APP_OUTPUT_DIR:-${repository_root}/dist}"
app_path="${output_root}/Redline.app"
version="${REDLINE_VERSION:-0.1.0}"
build_number="${REDLINE_BUILD_NUMBER:-1}"
sign_identity="${REDLINE_SIGN_IDENTITY:--}"
sparkle_feed_url="${REDLINE_SPARKLE_FEED_URL:-}"
sparkle_public_key="${REDLINE_SPARKLE_PUBLIC_KEY:-}"
temporary_root="$(mktemp -d "${TMPDIR:-/tmp}/redline-macos-build.XXXXXX")"
trap 'rm -rf "${temporary_root}"' EXIT

if [[ ! "${version}" =~ ^[0-9]+(\.[0-9]+){1,2}$ ]]; then
  printf 'REDLINE_VERSION must contain two or three numeric components (for example 1.2.0).\n' >&2
  exit 1
fi
if [[ ! "${build_number}" =~ ^[1-9][0-9]*$ ]]; then
  printf 'REDLINE_BUILD_NUMBER must be a positive integer.\n' >&2
  exit 1
fi
if [[ -n "${sparkle_feed_url}" || -n "${sparkle_public_key}" ]]; then
  if [[ -z "${sparkle_feed_url}" || -z "${sparkle_public_key}" ]]; then
    printf 'REDLINE_SPARKLE_FEED_URL and REDLINE_SPARKLE_PUBLIC_KEY must be set together.\n' >&2
    exit 1
  fi
  if [[ "${sparkle_feed_url}" != https://* ]]; then
    printf 'REDLINE_SPARKLE_FEED_URL must use HTTPS.\n' >&2
    exit 1
  fi
  if [[ "${sparkle_feed_url}" != *.xml || "${sparkle_feed_url}" == *\?* || "${sparkle_feed_url}" == *\#* ]]; then
    printf 'REDLINE_SPARKLE_FEED_URL must end in .xml without a query or fragment.\n' >&2
    exit 1
  fi
  public_key_bytes="${temporary_root}/sparkle-public-key"
  if ! printf '%s' "${sparkle_public_key}" | base64 -D > "${public_key_bytes}" 2>/dev/null ||
     [[ "$(wc -c < "${public_key_bytes}" | tr -d ' ')" != "32" ]]; then
    printf 'REDLINE_SPARKLE_PUBLIC_KEY must be a base64-encoded 32-byte Ed25519 public key.\n' >&2
    exit 1
  fi
fi
if [[ -n "${REDLINE_BUILD_ARCH:-}" ]]; then
  build_arch="${REDLINE_BUILD_ARCH}"
elif [[ "$(/usr/sbin/sysctl -n hw.optional.arm64 2>/dev/null || true)" == "1" ]]; then
  build_arch="arm64"
else
  build_arch="$(uname -m)"
fi

case "${build_arch}" in
  arm64) go_arch="arm64" ;;
  x86_64) go_arch="amd64" ;;
  *) printf 'Unsupported build architecture: %s\n' "${build_arch}" >&2; exit 1 ;;
esac

swift build --package-path "${repository_root}/macos" -c release --arch "${build_arch}"
swift_bin_path="$(swift build --package-path "${repository_root}/macos" -c release --arch "${build_arch}" --show-bin-path)"
GOARCH="${go_arch}" go build -trimpath -o "${temporary_root}/redline" "${repository_root}/cmd/redline"

if [[ -e "${app_path}" ]]; then
  rm -rf "${app_path}"
fi
sparkle_framework="${repository_root}/macos/.build/artifacts/sparkle/Sparkle/Sparkle.xcframework/macos-arm64_x86_64/Sparkle.framework"
if [[ ! -d "${sparkle_framework}" ]]; then
  printf 'Sparkle framework is missing; run swift package --package-path macos resolve.\n' >&2
  exit 1
fi
mkdir -p "${app_path}/Contents/MacOS" "${app_path}/Contents/Resources/bin" "${app_path}/Contents/Frameworks"
cp "${swift_bin_path}/RedlineMenuBar" "${app_path}/Contents/MacOS/RedlineMenuBar"
cp "${temporary_root}/redline" "${app_path}/Contents/Resources/bin/redline"
ditto "${sparkle_framework}" "${app_path}/Contents/Frameworks/Sparkle.framework"
cp "${repository_root}/macos/Sources/RedlineMenuBar/Resources/claude.svg" "${app_path}/Contents/Resources/claude.svg"
cp "${repository_root}/macos/Sources/RedlineMenuBar/Resources/AppIcon.icns" "${app_path}/Contents/Resources/AppIcon.icns"
cp "${repository_root}/config.example.yaml" "${app_path}/Contents/Resources/config.example.yaml"
cp "${repository_root}/macos/.build/artifacts/sparkle/Sparkle/LICENSE" "${app_path}/Contents/Resources/Sparkle-LICENSE"

swift_arch="$(lipo -archs "${app_path}/Contents/MacOS/RedlineMenuBar")"
service_arch="$(lipo -archs "${app_path}/Contents/Resources/bin/redline")"
if [[ "${swift_arch}" != "${build_arch}" || "${service_arch}" != "${build_arch}" ]]; then
  printf 'Architecture mismatch: app=%s service=%s expected=%s\n' "${swift_arch}" "${service_arch}" "${build_arch}" >&2
  exit 1
fi

info_plist="${app_path}/Contents/Info.plist"
plutil -create xml1 "${info_plist}"
plutil -insert CFBundleDevelopmentRegion -string en "${info_plist}"
plutil -insert CFBundleDisplayName -string Redline "${info_plist}"
plutil -insert CFBundleExecutable -string RedlineMenuBar "${info_plist}"
plutil -insert CFBundleIdentifier -string ai.redline.mac "${info_plist}"
plutil -insert CFBundleIconFile -string AppIcon "${info_plist}"
plutil -insert CFBundleInfoDictionaryVersion -string 6.0 "${info_plist}"
plutil -insert CFBundleName -string Redline "${info_plist}"
plutil -insert CFBundlePackageType -string APPL "${info_plist}"
plutil -insert CFBundleShortVersionString -string "${version}" "${info_plist}"
plutil -insert CFBundleVersion -string "${build_number}" "${info_plist}"
plutil -insert LSMinimumSystemVersion -string 13.0 "${info_plist}"
plutil -insert LSUIElement -bool true "${info_plist}"
plutil -insert NSAppTransportSecurity -xml '<dict><key>NSAllowsLocalNetworking</key><true/></dict>' "${info_plist}"
plutil -insert NSHumanReadableCopyright -string "Copyright © 2026 Redline" "${info_plist}"
if [[ -n "${sparkle_feed_url}" ]]; then
  plutil -insert SUFeedURL -string "${sparkle_feed_url}" "${info_plist}"
  plutil -insert SUPublicEDKey -string "${sparkle_public_key}" "${info_plist}"
fi

sign_options=(--force --options runtime --sign "${sign_identity}")
if [[ "${sign_identity}" == "-" ]]; then
  sign_options+=(--timestamp=none)
else
  sign_options+=(--timestamp)
fi

# Preserve Sparkle helper entitlements while signing all nested code with the
# same identity as Redline. Then seal the embedded service and outer app.
codesign "${sign_options[@]}" --deep --preserve-metadata=entitlements "${app_path}/Contents/Frameworks/Sparkle.framework"
codesign "${sign_options[@]}" "${app_path}/Contents/Resources/bin/redline"
if [[ "${sign_identity}" == "-" ]]; then
  local_entitlements="${temporary_root}/Redline.local.entitlements"
  plutil -create xml1 "${local_entitlements}"
  /usr/libexec/PlistBuddy -c 'Add :com.apple.security.cs.disable-library-validation bool true' "${local_entitlements}"
  codesign "${sign_options[@]}" --entitlements "${local_entitlements}" "${app_path}"
else
  codesign "${sign_options[@]}" "${app_path}"
fi
plutil -lint "${info_plist}"
codesign --verify --deep --strict "${app_path}"
if ! otool -l "${app_path}/Contents/MacOS/RedlineMenuBar" | grep -q '@executable_path/../Frameworks'; then
  printf 'RedlineMenuBar is missing its bundled-framework runtime search path.\n' >&2
  exit 1
fi

printf '%s\n' "Built ${app_path} (version ${version}, build ${build_number}, architecture ${build_arch})"
