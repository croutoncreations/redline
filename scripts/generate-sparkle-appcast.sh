#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
release_output_root="${REDLINE_RELEASE_OUTPUT_DIR:-${repository_root}/dist/releases}"
feed_url="${REDLINE_SPARKLE_FEED_URL:-}"
download_url_prefix="${REDLINE_SPARKLE_DOWNLOAD_URL_PREFIX:-${feed_url%/*}/}"
key_account="${REDLINE_SPARKLE_KEY_ACCOUNT:-ed25519}"
private_key_file="${REDLINE_SPARKLE_PRIVATE_KEY_FILE:-}"
generate_appcast="${REDLINE_SPARKLE_GENERATE_APPCAST:-${repository_root}/macos/.build/artifacts/sparkle/Sparkle/bin/generate_appcast}"
temporary_root="$(mktemp -d "${TMPDIR:-/tmp}/redline-appcast.XXXXXX")"
trap 'rm -rf "${temporary_root}"' EXIT

if [[ "${feed_url}" != https://* ]]; then
  printf 'REDLINE_SPARKLE_FEED_URL must be an HTTPS appcast URL.\n' >&2
  exit 1
fi
if [[ "${feed_url}" != *.xml || "${feed_url}" == *\?* || "${feed_url}" == *\#* ]]; then
  printf 'REDLINE_SPARKLE_FEED_URL must end in .xml without a query or fragment.\n' >&2
  exit 1
fi
if [[ "${download_url_prefix}" != https://* ]]; then
  printf 'REDLINE_SPARKLE_DOWNLOAD_URL_PREFIX must use HTTPS.\n' >&2
  exit 1
fi
if [[ ! -x "${generate_appcast}" ]]; then
  printf 'Sparkle generate_appcast tool is not executable: %s\n' "${generate_appcast}" >&2
  exit 1
fi
if [[ ! -d "${release_output_root}" ]]; then
  printf 'Release output directory does not exist: %s\n' "${release_output_root}" >&2
  exit 1
fi
if ! find "${release_output_root}" -maxdepth 1 -type f -name 'Redline-*.dmg' -print -quit | grep -q .; then
  printf 'No Redline release DMG exists in %s.\n' "${release_output_root}" >&2
  exit 1
fi

appcast_path="${release_output_root}/$(basename "${feed_url}")"
arguments=(
  --account "${key_account}"
  --download-url-prefix "${download_url_prefix}"
  -o "${appcast_path}"
)
if [[ -n "${private_key_file}" ]]; then
  if [[ ! -f "${private_key_file}" ]]; then
    printf 'Sparkle private key file does not exist: %s\n' "${private_key_file}" >&2
    exit 1
  fi
  decoded_private_key="${temporary_root}/private-key"
  modern_key_valid=0
  if base64 -D <"${private_key_file}" >"${decoded_private_key}" 2>/dev/null &&
     [[ "$(wc -c <"${decoded_private_key}" | tr -d ' ')" == "32" ]]; then
    modern_key_valid=1
  fi
  if [[ "${modern_key_valid}" != "1" ]] &&
     ! grep -Eq '^[[:xdigit:]]{128}$' "${private_key_file}"; then
    printf 'Sparkle private key file must contain a base64-encoded 32-byte Ed25519 private seed or a legacy 128-character key.\n' >&2
    exit 1
  fi
  arguments+=(--ed-key-file "${private_key_file}")
fi
arguments+=("${release_output_root}")

"${generate_appcast}" "${arguments[@]}"

if [[ ! -s "${appcast_path}" ]] || ! grep -q 'sparkle:edSignature=' "${appcast_path}"; then
  printf 'Generated appcast is missing an Ed25519 archive signature: %s\n' "${appcast_path}" >&2
  exit 1
fi
if ! grep -Fq "url=\"${download_url_prefix}" "${appcast_path}"; then
  printf 'Generated appcast does not use the configured HTTPS download prefix.\n' >&2
  exit 1
fi

printf 'Generated signed Sparkle appcast %s\n' "${appcast_path}"
