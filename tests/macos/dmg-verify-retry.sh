#!/usr/bin/env bash
# Unit test for verify_dmg in scripts/lib/macos-dmg.sh, with hdiutil and sleep
# stubbed so it runs anywhere (no macOS needed).
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/lib/macos-dmg.sh
source "${repository_root}/scripts/lib/macos-dmg.sh"

sleep() { :; }
script=()
# verify_dmg runs hdiutil inside $(...), a subshell, so the call count lives
# in a file. Each call consumes the next "exit:message" entry.
counter="$(mktemp)"
trap 'rm -f "${counter}"' EXIT
hdiutil() {
  local n
  n="$(cat "${counter}")"
  printf '%s' "$((n + 1))" > "${counter}"
  local entry="${script[$n]}"
  printf '%s\n' "${entry#*:}"
  return "${entry%%:*}"
}

fail=0
check() { # name, expected status, expected calls
  local name="$1" want_status="$2" want_calls="$3" status=0 calls
  printf '0' > "${counter}"
  verify_dmg /tmp/x.dmg 2>/dev/null || status=$?
  calls="$(cat "${counter}")"
  if [[ "${status}" -ne "${want_status}" || "${calls}" -ne "${want_calls}" ]]; then
    printf 'FAIL %s: status=%s calls=%s, want status=%s calls=%s\n' "${name}" "${status}" "${calls}" "${want_status}" "${want_calls}"
    fail=1
  else
    printf 'ok   %s\n' "${name}"
  fi
}

busy='1:hdiutil: verify: unable to recognize "/tmp/x.dmg" as a disk image. (Resource temporarily unavailable)'

script=('0:checksum is VALID')
check "passes first time" 0 1

script=("${busy}" '0:checksum is VALID')
check "retries the transient EAGAIN, then passes" 0 2

script=("${busy}" "${busy}" "${busy}")
check "gives up after three transient failures" 1 3

script=('1:hdiutil: verify: checksum of "/tmp/x.dmg" is INVALID')
check "a real failure fails at once" 1 1

script=('1:hdiutil: verify failed - image not recognized')
check "an unrecognized image without EAGAIN fails at once" 1 1

exit "${fail}"
