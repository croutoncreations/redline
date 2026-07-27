#!/usr/bin/env bash
set -euo pipefail

application="/Applications/Redline.app"
support_directory="${HOME}/Library/Application Support/Redline"
config_path="${support_directory}/redline.yaml"
database_path="${support_directory}/redline.db"
token_path="${support_directory}/api-token"
sentinel_task="release-rehearsal-state"
sentinel_profile="release-rehearsal-command"

validate_tag() {
  [[ "$1" =~ ^v[0-9]+(\.[0-9]+){1,2}$ ]] || {
    printf 'Release tag must look like v0.1.0: %s\n' "$1" >&2
    exit 1
  }
}

validate_version() {
  [[ "$1" =~ ^[0-9]+(\.[0-9]+){1,2}$ ]] || {
    printf 'Version must look like 0.1.1: %s\n' "$1" >&2
    exit 1
  }
}

app_version() {
  plutil -extract CFBundleShortVersionString raw "${application}/Contents/Info.plist"
}

redline_cli() {
  "${application}/Contents/Resources/bin/redline" "$@"
}

authenticated_health() {
  if [[ -s "${token_path}" ]]; then
    curl --fail --silent --show-error \
      -H "Authorization: Bearer $(<"${token_path}")" \
      http://127.0.0.1:7436/v1/health
  else
    curl --fail --silent --show-error http://127.0.0.1:7436/v1/health
  fi
}

wait_for_service() {
  local attempts=0
  until authenticated_health >/dev/null 2>&1; do
    attempts=$((attempts + 1))
    if [[ "${attempts}" -ge 120 ]]; then
      printf 'Redline service did not become ready.\n' >&2
      exit 1
    fi
    sleep 1
  done
}

verify_distribution() {
  codesign --verify --deep --strict "${application}"
  spctl --assess --type execute --verbose=2 "${application}"
  xcrun stapler validate "${application}"
}

install_dmg() {
  local dmg_path="$1"
  local mount_path
  mount_path="$(mktemp -d /tmp/redline-release-mount.XXXXXX)"
  hdiutil attach "${dmg_path}" -nobrowse -readonly -mountpoint "${mount_path}" -quiet
  if [[ ! -d "${mount_path}/Redline.app" ]]; then
    hdiutil detach "${mount_path}" -quiet
    printf 'Redline.app is missing from %s\n' "${dmg_path}" >&2
    exit 1
  fi
  rm -rf "${application}"
  ditto "${mount_path}/Redline.app" "${application}"
  hdiutil detach "${mount_path}" -quiet
  rmdir "${mount_path}"
}

launch_redline() {
  open "${application}"
  wait_for_service
}

download_release() {
  local tag="$1"
  local version="${tag#v}"
  local work_directory="$2"
  local release_root="https://github.com/croutoncreations/redline/releases/download/${tag}"
  local dmg_name="Redline-${version}-arm64.dmg"
  mkdir -p "${work_directory}"
  curl --fail --location --silent --show-error \
    --connect-timeout 10 --max-time 180 \
    --output "${work_directory}/${dmg_name}" "${release_root}/${dmg_name}" || return 1
  curl --fail --location --silent --show-error \
    --connect-timeout 10 --max-time 60 \
    --output "${work_directory}/${dmg_name}.sha256" "${release_root}/${dmg_name}.sha256" || return 1
  (
    cd "${work_directory}"
    shasum -a 256 -c "${dmg_name}.sha256" >&2
  )
  printf '%s\n' "${work_directory}/${dmg_name}"
}

seed_retained_state() {
  local profile_file
  local task_file
  profile_file="$(mktemp /tmp/redline-release-profile.XXXXXX.yaml)"
  task_file="$(mktemp /tmp/redline-release-task.XXXXXX.yaml)"
  mkdir -p /tmp/redline-release-rehearsal
  printf '%s\n' \
    "id: ${sentinel_profile}" \
    "provider_account_id: codex-main" \
    "harness_type: command" \
    "harness_command: /usr/bin/true" \
    "workspace_provider: existing-directory" \
    "repository: /tmp/redline-release-rehearsal" \
    "cleanup_policy: never" >"${profile_file}"
  printf '%s\n' \
    "id: ${sentinel_task}" \
    "name: Release rehearsal retained-state sentinel" \
    "type: one_off" \
    "priority: 1" \
    "dispatch_tier: expiring" \
    "execution_profile_id: ${sentinel_profile}" \
    "prompt: Do not run; this task verifies state retention across a Sparkle update." >"${task_file}"
  redline_cli profile add --file "${profile_file}" >/dev/null
  redline_cli task add --file "${task_file}" >/dev/null
  rm -f "${profile_file}" "${task_file}"
  redline_cli task list | grep -q "\"id\": \"${sentinel_task}\""
}

verify_retained_state() {
  [[ -s "${config_path}" ]]
  [[ -s "${database_path}" ]]
  [[ -s "${token_path}" ]]
  [[ "$(stat -f '%Lp' "${token_path}")" == "600" ]]
  redline_cli task list | grep -q "\"id\": \"${sentinel_task}\""
  redline_cli profile list | grep -q "\"id\": \"${sentinel_profile}\""
  authenticated_health >/dev/null
  local service_count
  service_count="$(pgrep -f '/Redline.app/Contents/Resources/bin/redline.*serve' | wc -l | tr -d ' ')"
  [[ "${service_count}" == "1" ]] || {
    printf 'Expected one Redline service, found %s.\n' "${service_count}" >&2
    exit 1
  }
}

action="${1:-}"
case "${action}" in
  prepare-upgrade)
    [[ "$#" -eq 2 ]] || exit 1
    validate_tag "$2"
    work_directory="${HOME}/Downloads/Redline Release Rehearsal"
    dmg_path="$(download_release "$2" "${work_directory}")"
    install_dmg "${dmg_path}"
    verify_distribution
    launch_redline
    seed_retained_state
    printf 'Prepared Redline %s with retained-state sentinels.\n' "$(app_version)"
    ;;
  verify-upgrade)
    [[ "$#" -eq 2 ]] || exit 1
    validate_version "$2"
    [[ "$(app_version)" == "$2" ]] || {
      printf 'Expected Redline %s, found %s.\n' "$2" "$(app_version)" >&2
      exit 1
    }
    wait_for_service
    verify_distribution
    verify_retained_state
    printf 'Verified Sparkle update to %s and retained Redline state.\n' "$2"
    ;;
  prepare-candidate)
    [[ "$#" -eq 3 ]] || exit 1
    validate_version "$3"
    [[ -f "$2" ]] || { printf 'Candidate DMG is unavailable in the guest: %s\n' "$2" >&2; exit 1; }
    if [[ -f "${2}.sha256" ]]; then
      (
        cd "$(dirname "$2")"
        shasum -a 256 -c "$(basename "$2").sha256"
      )
    fi
    spctl --assess --type open --context context:primary-signature --verbose=2 "$2"
    install_dmg "$2"
    [[ "$(app_version)" == "$3" ]]
    verify_distribution
    launch_redline
    [[ -s "${config_path}" ]]
    [[ -s "${token_path}" ]]
    [[ "$(stat -f '%Lp' "${token_path}")" == "600" ]]
    printf 'Verified clean installation and first launch of Redline %s.\n' "$3"
    ;;
  *)
    printf 'usage: tart-guest-release-check.sh <prepare-upgrade|verify-upgrade|prepare-candidate> ...\n' >&2
    exit 1
    ;;
esac
