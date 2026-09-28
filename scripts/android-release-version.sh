#!/usr/bin/env bash
# Prints the versionName and versionCode an Android release is built with,
# derived from its mobile-v<version> tag, as GITHUB_OUTPUT lines:
#
#   name=<version>
#   code=<versionCode>
#
# Kept out of the workflow so tests/android/release-version.sh can exercise
# it: both rules below had holes that only showed up when tested directly.
set -euo pipefail

tag="${1:?usage: android-release-version.sh mobile-v<version>}"
version="${tag#mobile-v}"
if [[ -z "${version}" || "${version}" == "${tag}" ]]; then
  printf 'tag %q does not match mobile-v<version>\n' "${tag}" >&2
  exit 1
fi

# Semantic versioning as semver.org defines it: numeric parts without leading
# zeros, and dot-separated prerelease identifiers that are non-empty and,
# when purely numeric, also without leading zeros. Build metadata ("+...") is
# not accepted -- Play shows versionName to people, and a tag is the wrong
# place for it. A looser pattern let 01.2.3 and 1.2.3-.. through.
numeric='(0|[1-9][0-9]*)'
identifier='(0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)'
if [[ ! "${version}" =~ ^${numeric}\.${numeric}\.${numeric}(-${identifier}(\.${identifier})*)?$ ]]; then
  printf 'tag version %q is not a valid semantic version (expected mobile-vMAJOR.MINOR.PATCH[-prerelease])\n' "${version}" >&2
  exit 1
fi

# Play requires every upload for an application id to carry a strictly larger
# versionCode than the last. Seconds since a fixed epoch (2023-11-14, Unix
# time 1,700,000,000) give that: two releases, or a rerun after an upload,
# get distinct increasing codes unless they start in the same second.
# Minutes, used before, repeated for two releases in one minute and the
# second upload failed. This epoch keeps every code above any the minute
# scheme produced (about 29.8 million in 2026) and below Play's documented
# 2,100,000,000 ceiling until about 2090.
now="${REDLINE_RELEASE_NOW:-$(date -u +%s)}"
if [[ ! "${now}" =~ ^[0-9]+$ ]]; then
  printf 'REDLINE_RELEASE_NOW %q is not a Unix time\n' "${now}" >&2
  exit 1
fi
code=$(( now - 1700000000 ))
if (( code < 1 || code > 2100000000 )); then
  printf 'derived versionCode %s is outside Play'\''s valid range (1-2100000000)\n' "${code}" >&2
  exit 1
fi

printf 'name=%s\ncode=%s\n' "${version}" "${code}"
