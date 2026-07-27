#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
host_script="${repository_root}/scripts/rehearse-macos-release-vm.sh"
guest_script="${repository_root}/tests/macos/tart-guest-release-check.sh"

plan="$(
  REDLINE_TART_BASE_VM=redline-test-base \
  REDLINE_TART_TEST_VM=redline-test-run \
  REDLINE_TART_BASE_IMAGE=ghcr.io/example/macos:latest \
    "${host_script}" plan-upgrade v0.1.0 0.1.1
)"
grep -q 'Base image: ghcr.io/example/macos:latest' <<<"${plan}"
grep -q 'Base VM: redline-test-base' <<<"${plan}"
grep -q 'Disposable VM: redline-test-run' <<<"${plan}"
grep -q 'Install baseline: v0.1.0' <<<"${plan}"
grep -q 'Verify updated version: 0.1.1' <<<"${plan}"

if REDLINE_TART_TEST_VM=unsafe-name "${host_script}" plan-upgrade v0.1.0 0.1.1 >/dev/null 2>&1; then
  printf 'host rehearsal unexpectedly accepted a VM outside the redline- namespace\n' >&2
  exit 1
fi
if "${host_script}" plan-upgrade release-1 0.1.1 >/dev/null 2>&1; then
  printf 'host rehearsal unexpectedly accepted an invalid release tag\n' >&2
  exit 1
fi
if "${host_script}" prepare-upgrade-from-dmg /does/not/exist.dmg 0.1.0 >/dev/null 2>&1; then
  printf 'host rehearsal unexpectedly accepted a missing baseline DMG\n' >&2
  exit 1
fi
if "${guest_script}" verify-upgrade invalid-version >/dev/null 2>&1; then
  printf 'guest verification unexpectedly accepted an invalid version\n' >&2
  exit 1
fi

printf 'Tart release rehearsal command validation passed.\n'
