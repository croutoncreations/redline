#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
guest_script="${repository_root}/tests/macos/tart-guest-release-check.sh"
tart_bin="${TART_BIN:-}"
base_image="${REDLINE_TART_BASE_IMAGE:-ghcr.io/cirruslabs/macos-sequoia-base:latest}"
base_vm="${REDLINE_TART_BASE_VM:-redline-sequoia-base}"
test_vm="${REDLINE_TART_TEST_VM:-redline-release-test}"
replace_vm="${REDLINE_TART_REPLACE:-0}"
state_root="${REDLINE_TART_STATE_DIR:-${TMPDIR:-/tmp}/redline-tart-release}"
vm_log="${state_root}/${test_vm}.log"
vm_error_log="${state_root}/${test_vm}.error.log"
launch_label="ai.redline.release-vm.${test_vm}"

usage() {
  cat <<'EOF'
usage:
  rehearse-macos-release-vm.sh plan-upgrade <baseline-tag> <expected-version>
  rehearse-macos-release-vm.sh bootstrap
  rehearse-macos-release-vm.sh prepare-upgrade <baseline-tag>
  rehearse-macos-release-vm.sh verify-upgrade <expected-version>
  rehearse-macos-release-vm.sh prepare-candidate <dmg-path> <expected-version>
  rehearse-macos-release-vm.sh open
  rehearse-macos-release-vm.sh status
  rehearse-macos-release-vm.sh stop
  rehearse-macos-release-vm.sh destroy

Set REDLINE_TART_REPLACE=1 to replace an existing disposable test VM.
The base VM is retained across rehearsals and is never deleted by this script.
EOF
}

validate_vm_name() {
  local name="$1"
  if [[ ! "${name}" =~ ^redline-[a-z0-9][a-z0-9-]*$ ]]; then
    printf 'Tart VM names must remain in the redline- namespace: %s\n' "${name}" >&2
    exit 1
  fi
}

validate_tag() {
  if [[ ! "$1" =~ ^v[0-9]+(\.[0-9]+){1,2}$ ]]; then
    printf 'Release tag must look like v0.1.0: %s\n' "$1" >&2
    exit 1
  fi
}

validate_version() {
  if [[ ! "$1" =~ ^[0-9]+(\.[0-9]+){1,2}$ ]]; then
    printf 'Version must look like 0.1.1: %s\n' "$1" >&2
    exit 1
  fi
}

require_tart() {
  if [[ -z "${tart_bin}" ]]; then
    tart_bin="$(command -v tart || true)"
  fi
  if [[ -z "${tart_bin}" || ! -x "${tart_bin}" ]]; then
    printf 'Tart is required. See https://tart.run/quick-start/.\n' >&2
    exit 1
  fi
  if file "${tart_bin}" | grep -q 'shell script'; then
    local brewed_tart
    brewed_tart="$(brew --prefix tart 2>/dev/null || true)"
    if [[ -x "${brewed_tart}/libexec/tart.app/Contents/MacOS/tart" ]]; then
      tart_bin="${brewed_tart}/libexec/tart.app/Contents/MacOS/tart"
    fi
  fi
  if [[ "$(sysctl -n hw.optional.arm64 2>/dev/null || printf '0')" != "1" ]]; then
    printf 'Tart macOS VMs require an Apple Silicon host.\n' >&2
    exit 1
  fi
}

run_tart() {
  /usr/bin/arch -arm64 "${tart_bin}" "$@"
}

run_tart_bounded() {
  local limit_seconds="$1"
  shift
  run_tart "$@" &
  local command_pid="$!"
  (
    sleep "${limit_seconds}"
    kill -TERM "${command_pid}" >/dev/null 2>&1 || true
  ) &
  local timer_pid="$!"
  local status=0
  if wait "${command_pid}"; then
    status=0
  else
    status="$?"
  fi
  kill -TERM "${timer_pid}" >/dev/null 2>&1 || true
  wait "${timer_pid}" >/dev/null 2>&1 || true
  return "${status}"
}

vm_exists() {
  run_tart_bounded 5 get "$1" >/dev/null 2>&1
}

ensure_base() {
  if vm_exists "${base_vm}"; then
    return
  fi
  printf 'Creating reusable Tart base %s from %s...\n' "${base_vm}" "${base_image}"
  run_tart clone "${base_image}" "${base_vm}"
}

replace_disposable_if_requested() {
  if ! vm_exists "${test_vm}"; then
    return
  fi
  if [[ "${replace_vm}" != "1" ]]; then
    printf 'Disposable VM %s already exists. Use REDLINE_TART_REPLACE=1 to replace it.\n' "${test_vm}" >&2
    exit 1
  fi
  run_tart_bounded 30 stop "${test_vm}" >/dev/null 2>&1 || true
  launchctl remove "${launch_label}" >/dev/null 2>&1 || true
  run_tart delete "${test_vm}"
}

clone_disposable() {
  replace_disposable_if_requested
  run_tart clone "${base_vm}" "${test_vm}"
  run_tart set "${test_vm}" --cpu 4 --memory 8192 --display 1440x900
}

start_vm() {
  mkdir -p "${state_root}"
  : >"${vm_log}"
  : >"${vm_error_log}"
  local -a arguments=(run --no-audio)
  if [[ "$#" -gt 0 ]]; then
    arguments+=("--dir=candidate:$1:ro")
  fi
  launchctl remove "${launch_label}" >/dev/null 2>&1 || true
  launchctl submit -l "${launch_label}" -o "${vm_log}" -e "${vm_error_log}" -- \
    /usr/bin/arch -arm64 "${tart_bin}" "${arguments[@]}" "${test_vm}"
}

wait_for_guest() {
  local attempts=0
  until run_tart_bounded 10 exec "${test_vm}" /usr/bin/true >/dev/null 2>&1; do
    attempts=$((attempts + 1))
    if [[ "${attempts}" -ge 180 ]]; then
      printf 'VM did not become ready; see %s\n' "${vm_log}" >&2
      exit 1
    fi
    sleep 2
  done
}

wait_for_guest_network() {
  local attempts=0
  while ! run_tart_bounded 10 exec "${test_vm}" /usr/bin/curl --head --fail --silent \
    --connect-timeout 3 --max-time 5 https://github.com >/dev/null 2>&1; do
    attempts=$((attempts + 1))
    if [[ "${attempts}" -eq 3 ]]; then
      printf 'Guest DNS is unavailable; applying public DNS to the disposable VM.\n'
      run_tart exec "${test_vm}" /usr/bin/sudo -n /usr/sbin/networksetup \
        -setdnsservers Ethernet 1.1.1.1 8.8.8.8
    fi
    if [[ "${attempts}" -ge 30 ]]; then
      printf 'VM network did not become ready; see %s\n' "${vm_error_log}" >&2
      exit 1
    fi
    sleep 2
  done
}

run_guest() {
  run_tart_bounded 600 exec -i "${test_vm}" /bin/bash -s -- "$@" <"${guest_script}"
}

validate_vm_name "${base_vm}"
validate_vm_name "${test_vm}"
if [[ "${base_vm}" == "${test_vm}" ]]; then
  printf 'Base and disposable VM names must differ.\n' >&2
  exit 1
fi

action="${1:-}"
case "${action}" in
  plan-upgrade)
    [[ "$#" -eq 3 ]] || { usage >&2; exit 1; }
    validate_tag "$2"
    validate_version "$3"
    cat <<EOF
Base image: ${base_image}
Base VM: ${base_vm}
Disposable VM: ${test_vm}
Install baseline: $2
Manual checkpoint: verify first-run UI, then use Redline > Check for Updates
Verify updated version: $3
Retained state: config, SQLite database, API credential, and sentinel task
Cleanup: delete only ${test_vm}; retain ${base_vm}
EOF
    ;;
  bootstrap)
    [[ "$#" -eq 1 ]] || { usage >&2; exit 1; }
    require_tart
    ensure_base
    ;;
  prepare-upgrade)
    [[ "$#" -eq 2 ]] || { usage >&2; exit 1; }
    validate_tag "$2"
    require_tart
    ensure_base
    clone_disposable
    start_vm
    wait_for_guest
    wait_for_guest_network
    run_guest prepare-upgrade "$2"
    cat <<EOF

Baseline $2 is installed in ${test_vm}, and retained-state sentinels are ready.
In the VM:
  1. Inspect the first-run and menu-bar experience.
  2. After the next release is published, choose Redline > Check for Updates.
  3. Let Sparkle replace and relaunch the app.
Then run:
  $0 verify-upgrade <new-version>
EOF
    ;;
  verify-upgrade)
    [[ "$#" -eq 2 ]] || { usage >&2; exit 1; }
    validate_version "$2"
    require_tart
    vm_exists "${test_vm}" || { printf 'Disposable VM %s does not exist.\n' "${test_vm}" >&2; exit 1; }
    wait_for_guest
    run_guest verify-upgrade "$2"
    ;;
  prepare-candidate)
    [[ "$#" -eq 3 ]] || { usage >&2; exit 1; }
    candidate_path="$(cd "$(dirname "$2")" && pwd)/$(basename "$2")"
    [[ -f "${candidate_path}" ]] || { printf 'Candidate DMG does not exist: %s\n' "${candidate_path}" >&2; exit 1; }
    [[ "${candidate_path}" != *:* ]] || { printf 'Candidate path cannot contain a colon.\n' >&2; exit 1; }
    validate_version "$3"
    require_tart
    ensure_base
    clone_disposable
    start_vm "$(dirname "${candidate_path}")"
    wait_for_guest
    run_guest prepare-candidate "/Volumes/My Shared Files/candidate/$(basename "${candidate_path}")" "$3"
    ;;
  open)
    [[ "$#" -eq 1 ]] || { usage >&2; exit 1; }
    require_tart
    vm_exists "${test_vm}" || { printf 'Disposable VM %s does not exist.\n' "${test_vm}" >&2; exit 1; }
    start_vm
    ;;
  status)
    [[ "$#" -eq 1 ]] || { usage >&2; exit 1; }
    require_tart
    run_tart list
    if [[ -f "${vm_log}" ]]; then
      printf '\nVM log: %s\n' "${vm_log}"
      tail -20 "${vm_log}"
    fi
    if [[ -f "${vm_error_log}" ]]; then
      printf '\nVM error log: %s\n' "${vm_error_log}"
      tail -20 "${vm_error_log}"
    fi
    ;;
  stop)
    [[ "$#" -eq 1 ]] || { usage >&2; exit 1; }
    require_tart
    vm_exists "${test_vm}" || exit 0
    run_tart_bounded 30 stop "${test_vm}"
    launchctl remove "${launch_label}" >/dev/null 2>&1 || true
    ;;
  destroy)
    [[ "$#" -eq 1 ]] || { usage >&2; exit 1; }
    require_tart
    if vm_exists "${test_vm}"; then
      run_tart_bounded 30 stop "${test_vm}" >/dev/null 2>&1 || true
      launchctl remove "${launch_label}" >/dev/null 2>&1 || true
      run_tart delete "${test_vm}"
    fi
    rm -f "${vm_log}" "${vm_error_log}"
    ;;
  *)
    usage >&2
    exit 1
    ;;
esac
