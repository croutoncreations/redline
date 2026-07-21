#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output_root="${REDLINE_APP_OUTPUT_DIR:-${repository_root}/dist}"
app_path="${output_root}/Redline.app"
temporary_root="$(mktemp -d "${TMPDIR:-/tmp}/redline-macos-build.XXXXXX")"
trap 'rm -rf "${temporary_root}"' EXIT
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
mkdir -p "${app_path}/Contents/MacOS" "${app_path}/Contents/Resources/bin"
cp "${swift_bin_path}/RedlineMenuBar" "${app_path}/Contents/MacOS/RedlineMenuBar"
cp "${temporary_root}/redline" "${app_path}/Contents/Resources/bin/redline"
cp "${repository_root}/macos/Sources/RedlineMenuBar/Resources/claude.svg" "${app_path}/Contents/Resources/claude.svg"
cp "${repository_root}/macos/Sources/RedlineMenuBar/Resources/chatgpt.svg" "${app_path}/Contents/Resources/chatgpt.svg"

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
plutil -insert CFBundleInfoDictionaryVersion -string 6.0 "${info_plist}"
plutil -insert CFBundleName -string Redline "${info_plist}"
plutil -insert CFBundlePackageType -string APPL "${info_plist}"
plutil -insert CFBundleShortVersionString -string 0.1.0 "${info_plist}"
plutil -insert CFBundleVersion -string 1 "${info_plist}"
plutil -insert LSMinimumSystemVersion -string 13.0 "${info_plist}"
plutil -insert LSUIElement -bool true "${info_plist}"
plutil -insert NSAppTransportSecurity -xml '<dict><key>NSAllowsLocalNetworking</key><true/></dict>' "${info_plist}"
plutil -insert NSHumanReadableCopyright -string "Copyright © 2026 Redline" "${info_plist}"

codesign --force --deep --sign "${REDLINE_SIGN_IDENTITY:--}" "${app_path}"
plutil -lint "${info_plist}"
codesign --verify --deep --strict "${app_path}"

printf '%s\n' "Built ${app_path}"
