#!/usr/bin/env bash
# Tests scripts/android-release-version.sh, which turns a mobile-v* tag into
# the versionName and versionCode the Play upload is built with.
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
script="${repository_root}/scripts/android-release-version.sh"
failures=0

accepts() {
  local tag="$1" want="$2" out
  if ! out="$(REDLINE_RELEASE_NOW=1790563986 "${script}" "${tag}" </dev/null 2>&1)"; then
    printf 'FAIL: %s rejected: %s\n' "${tag}" "${out}" >&2; failures=$((failures + 1)); return
  fi
  if ! grep -qx "name=${want}" <<<"${out}"; then
    printf 'FAIL: %s gave %s, want name=%s\n' "${tag}" "${out}" "${want}" >&2; failures=$((failures + 1))
  fi
}

rejects() {
  local tag="$1"
  if REDLINE_RELEASE_NOW=1790563986 "${script}" "${tag}" </dev/null >/dev/null 2>&1; then
    printf 'FAIL: %s was accepted\n' "${tag}" >&2; failures=$((failures + 1))
  fi
}

code_at() { REDLINE_RELEASE_NOW="$1" "${script}" mobile-v1.2.3 </dev/null | sed -n 's/^code=//p'; }

# Semantic versions, per semver.org: no leading zeros in numeric parts or
# numeric prerelease identifiers, and no empty prerelease identifiers.
accepts mobile-v0.2.0 0.2.0
accepts mobile-v1.10.0 1.10.0
accepts mobile-v1.2.3-rc.1 1.2.3-rc.1
accepts mobile-v1.2.3-0 1.2.3-0
accepts mobile-v1.2.3-alpha-2.0a 1.2.3-alpha-2.0a
for bad in mobile-v mobile-v1.2 mobile-v01.2.3 mobile-v1.02.3 mobile-v1.2.03 \
  mobile-v1.2.3-01 mobile-v1.2.3-.. mobile-v1.2.3-rc. mobile-v1.2.3- \
  "mobile-v1.2.3 " mobile-vv1.2.3 mobile-v1.2.3+build.1 v1.2.3; do
  rejects "${bad}"
done

# versionCode must strictly increase between any two releases, including two
# in the same minute, and must stay above every code the earlier
# minutes-since-epoch scheme could have produced.
first="$(code_at 1790563986)"
second="$(code_at 1790563987)"
if (( second <= first )); then
  printf 'FAIL: releases one second apart got %s then %s\n' "${first}" "${second}" >&2; failures=$((failures + 1))
fi
if (( first <= 1790563986 / 60 )); then
  printf 'FAIL: code %s is not above the old minute-based code\n' "${first}" >&2; failures=$((failures + 1))
fi
# Play's ceiling is 2,100,000,000: refuse rather than upload past it.
if REDLINE_RELEASE_NOW=4000000000 "${script}" mobile-v1.2.3 </dev/null >/dev/null 2>&1; then
  printf 'FAIL: a code past the Play ceiling was accepted\n' >&2; failures=$((failures + 1))
fi

# An override too large for bash arithmetic must be refused, not wrap around
# into a code that passes the range check: 2^64 + 1,700,001,000 wraps to 1000.
for bad_now in 18446744075409552616 99999999999999999999 abc; do
  if REDLINE_RELEASE_NOW="${bad_now}" "${script}" mobile-v1.2.3 </dev/null >/dev/null 2>&1; then
    printf 'FAIL: REDLINE_RELEASE_NOW=%q was accepted\n' "${bad_now}" >&2; failures=$((failures + 1))
  fi
done

# Only the newest mobile-v* tag may release. A rerun of an older tag would
# otherwise get a newer time-derived versionCode and replace a newer build as
# the latest on Play. Tags arrive on stdin, one per line (git tag -l output).
newest() { REDLINE_RELEASE_NOW=1790563986 "${script}" "$1" <<<"$2" >/dev/null 2>&1; }
check_newest() {
  local verdict="$1" tag="$2" existing="$3"
  if newest "${tag}" "${existing}"; then got=allowed; else got=refused; fi
  if [[ "${got}" != "${verdict}" ]]; then
    printf 'FAIL: %s with existing tags [%s] was %s, want %s\n' "${tag}" "${existing//$'\n'/ }" "${got}" "${verdict}" >&2
    failures=$((failures + 1))
  fi
}
check_newest allowed mobile-v0.3.0 $'mobile-v0.2.0\nmobile-v0.3.0'
check_newest refused mobile-v0.2.0 $'mobile-v0.2.0\nmobile-v0.3.0'
check_newest allowed mobile-v0.3.0 ''
# Numeric, not lexical: 0.10.0 is newer than 0.9.0.
check_newest allowed mobile-v0.10.0 $'mobile-v0.9.0\nmobile-v0.10.0'
check_newest refused mobile-v0.9.0 $'mobile-v0.9.0\nmobile-v0.10.0'
# A release outranks its own prereleases, and rc.10 outranks rc.9.
check_newest refused mobile-v1.0.0-rc.1 $'mobile-v1.0.0-rc.1\nmobile-v1.0.0'
check_newest allowed mobile-v1.0.0 $'mobile-v1.0.0-rc.1\nmobile-v1.0.0'
check_newest refused mobile-v1.0.0-rc.9 $'mobile-v1.0.0-rc.9\nmobile-v1.0.0-rc.10'
check_newest refused mobile-v1.0.0-alpha $'mobile-v1.0.0-alpha\nmobile-v1.0.0-beta'
# Numeric identifiers rank below alphanumeric ones.
check_newest refused mobile-v1.0.0-1 $'mobile-v1.0.0-1\nmobile-v1.0.0-alpha'
# Numbers too big for bash arithmetic still compare by value, not by wrapped
# value: semver puts no bound on them.
check_newest refused mobile-v1.0.0 $'mobile-v1.0.0\nmobile-v9223372036854775808.0.0'
check_newest refused mobile-v1.0.0-1 $'mobile-v1.0.0-1\nmobile-v1.0.0-99999999999999999999'
# Alphanumeric identifiers compare in ASCII order, whatever the locale:
# uppercase sorts before lowercase, so "a" is newer than "Z".
LC_ALL=en_US.UTF-8 check_newest refused mobile-v1.0.0-Z $'mobile-v1.0.0-Z\nmobile-v1.0.0-a'
# A malformed tag elsewhere in the repo is ignored, not a reason to refuse.
check_newest allowed mobile-v0.3.0 $'mobile-v0.3.0\nmobile-v9.9\nmobile-vjunk'

if (( failures > 0 )); then
  printf '%d release-version check(s) failed.\n' "${failures}" >&2
  exit 1
fi
printf 'Android release version tests passed.\n'
