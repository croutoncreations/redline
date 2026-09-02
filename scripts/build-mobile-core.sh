#!/usr/bin/env bash
set -euo pipefail

# Builds the shared Go core into a platform binding.
#
# The output is a build artifact and is not committed: it is roughly 19MB and
# is fully reproducible from mobile/core.
#
#   ./scripts/build-mobile-core.sh            # android (default)
#   ./scripts/build-mobile-core.sh ios
#
# Requires gomobile:
#   go install golang.org/x/mobile/cmd/gomobile@latest && gomobile init

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
target="${1:-android}"
output_root="${REDLINE_MOBILE_OUTPUT_DIR:-${repository_root}/mobile/build}"

if ! command -v gomobile >/dev/null 2>&1; then
  # gomobile installs into GOBIN or GOPATH/bin, which is not always on PATH.
  gobin="$(go env GOBIN)"
  if [[ -z "${gobin}" ]]; then
    gobin="$(go env GOPATH)/bin"
  fi
  if [[ -x "${gobin}/gomobile" ]]; then
    PATH="${PATH}:${gobin}"
  else
    printf 'gomobile is not installed. Run:\n' >&2
    printf '  go install golang.org/x/mobile/cmd/gomobile@latest && gomobile init\n' >&2
    exit 1
  fi
fi

if [[ "${target}" == "android" ]]; then
  : "${ANDROID_HOME:=${HOME}/Library/Android/sdk}"
  export ANDROID_HOME
  if [[ -z "${ANDROID_NDK_HOME:-}" ]]; then
    # gomobile needs an explicit NDK; pick the highest installed version rather
    # than pinning one that may not exist on another machine.
    ndk_root="${ANDROID_HOME}/ndk"
    if [[ -d "${ndk_root}" ]]; then
      ANDROID_NDK_HOME="${ndk_root}/$(ls "${ndk_root}" | sort -V | tail -1)"
      export ANDROID_NDK_HOME
    fi
  fi
  if [[ ! -d "${ANDROID_NDK_HOME:-}" ]]; then
    printf 'No Android NDK found. Install one via Android Studio, or set ANDROID_NDK_HOME.\n' >&2
    exit 1
  fi
fi

mkdir -p "${output_root}"

case "${target}" in
  android)
    output="${output_root}/redlinecore.aar"
    # API 26 matches the app's minSdk.
    gomobile bind -target=android -androidapi 26 -o "${output}" ./mobile/core
    ;;
  ios)
    output="${output_root}/RedlineCore.xcframework"
    gomobile bind -target=ios -o "${output}" ./mobile/core
    ;;
  *)
    printf 'Unsupported target %q. Use android or ios.\n' "${target}" >&2
    exit 1
    ;;
esac

printf 'Built %s\n' "${output}"
