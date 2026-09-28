#!/usr/bin/env bash
# Tests scripts/android-release-version.sh, which turns a mobile-v* tag into
# the versionName and versionCode the Play upload is built with.
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
script="${repository_root}/scripts/android-release-version.sh"
failures=0

accepts() {
  local tag="$1" want="$2" out
  if ! out="$(REDLINE_RELEASE_NOW=1790563986 "${script}" "${tag}" 2>&1)"; then
    printf 'FAIL: %s rejected: %s\n' "${tag}" "${out}" >&2; failures=$((failures + 1)); return
  fi
  if ! grep -qx "name=${want}" <<<"${out}"; then
    printf 'FAIL: %s gave %s, want name=%s\n' "${tag}" "${out}" "${want}" >&2; failures=$((failures + 1))
  fi
}

rejects() {
  local tag="$1"
  if REDLINE_RELEASE_NOW=1790563986 "${script}" "${tag}" >/dev/null 2>&1; then
    printf 'FAIL: %s was accepted\n' "${tag}" >&2; failures=$((failures + 1))
  fi
}

code_at() { REDLINE_RELEASE_NOW="$1" "${script}" mobile-v1.2.3 | sed -n 's/^code=//p'; }

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
if REDLINE_RELEASE_NOW=4000000000 "${script}" mobile-v1.2.3 >/dev/null 2>&1; then
  printf 'FAIL: a code past the Play ceiling was accepted\n' >&2; failures=$((failures + 1))
fi

if (( failures > 0 )); then
  printf '%d release-version check(s) failed.\n' "${failures}" >&2
  exit 1
fi
printf 'Android release version tests passed.\n'
