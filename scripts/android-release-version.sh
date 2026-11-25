#!/usr/bin/env bash
# Prints the versionName and versionCode an Android release is built with,
# derived from its mobile-v<version> tag, as GITHUB_OUTPUT lines. The
# repository's mobile-v* tags are read from stdin (git tag -l 'mobile-v*'),
# and anything but the newest is refused; see below.
#
#   name=<version>
#   code=<versionCode>
#
# Kept out of the workflow so tests/android/release-version.sh can exercise
# it: both rules below had holes that only showed up when tested directly.
set -euo pipefail
# Every string comparison below means ASCII order, as semver specifies;
# under a UTF-8 locale [[ < ]] would collate "Z" after "a".
export LC_ALL=C

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

# semver_compare A B prints -1, 0 or 1 as version A sorts before, equal to,
# or after B, by semver.org precedence: numeric core parts compared as
# numbers, a version with a prerelease below the same version without, and
# prerelease identifiers compared one by one -- numeric ones as numbers and
# below alphanumeric ones, which compare as ASCII text.
# numeric_compare A B compares two digit strings by value without bash
# arithmetic, which overflows past 2^63 while semver bounds nothing. Leading
# zeros cannot occur -- the tag pattern forbids them -- so the longer string
# is the larger number, and equal lengths compare digit by digit.
numeric_compare() {
  if (( ${#1} != ${#2} )); then
    (( ${#1} < ${#2} )) && echo -1 || echo 1
  elif [[ "$1" == "$2" ]]; then echo 0
  elif [[ "$1" < "$2" ]]; then echo -1
  else echo 1
  fi
}

semver_compare() {
  local a_core="${1%%-*}" b_core="${2%%-*}" a_pre="" b_pre=""
  [[ "$1" == *-* ]] && a_pre="${1#*-}"
  [[ "$2" == *-* ]] && b_pre="${2#*-}"
  local -a a_parts b_parts
  IFS=. read -r -a a_parts <<<"${a_core}"
  IFS=. read -r -a b_parts <<<"${b_core}"
  local i
  local order
  for i in 0 1 2; do
    order="$(numeric_compare "${a_parts[i]}" "${b_parts[i]}")"
    if [[ "${order}" != 0 ]]; then echo "${order}"; return; fi
  done
  if [[ -z "${a_pre}" || -z "${b_pre}" ]]; then
    if [[ "${a_pre}" == "${b_pre}" ]]; then echo 0
    elif [[ -z "${a_pre}" ]]; then echo 1
    else echo -1
    fi
    return
  fi
  IFS=. read -r -a a_parts <<<"${a_pre}"
  IFS=. read -r -a b_parts <<<"${b_pre}"
  for (( i = 0; i < ${#a_parts[@]} && i < ${#b_parts[@]}; i++ )); do
    local x="${a_parts[i]}" y="${b_parts[i]}"
    [[ "${x}" == "${y}" ]] && continue
    if [[ "${x}" =~ ^[0-9]+$ && "${y}" =~ ^[0-9]+$ ]]; then
      numeric_compare "${x}" "${y}"
    elif [[ "${x}" =~ ^[0-9]+$ ]]; then echo -1
    elif [[ "${y}" =~ ^[0-9]+$ ]]; then echo 1
    elif [[ "${x}" < "${y}" ]]; then echo -1
    else echo 1
    fi
    return
  done
  if (( ${#a_parts[@]} == ${#b_parts[@]} )); then echo 0
  elif (( ${#a_parts[@]} < ${#b_parts[@]} )); then echo -1
  else echo 1
  fi
}

# Only the newest tag may release. versionCode comes from the clock, not the
# tag, so rerunning an older tag -- one displaced from the release queue, say
# -- would give it a newer code than a release already on Play, and the older
# build would become the latest. Refusing here means the fix for a missed
# release is always a new tag. Malformed tags elsewhere in the repository are
# not this release's business and are skipped.
while IFS= read -r other || [[ -n "${other}" ]]; do
  other_version="${other#mobile-v}"
  [[ "${other_version}" == "${other}" ]] && continue
  [[ "${other_version}" =~ ^${numeric}\.${numeric}\.${numeric}(-${identifier}(\.${identifier})*)?$ ]] || continue
  if [[ "$(semver_compare "${version}" "${other_version}")" == -1 ]]; then
    printf 'refusing to release %s: %s is newer. Release a new tag instead; see docs/android-release.md.\n' "${tag}" "${other}" >&2
    exit 1
  fi
done

# Play requires every upload for an application id to carry a strictly larger
# versionCode than the last. Seconds since a fixed epoch (2023-11-14, Unix
# time 1,700,000,000) give that: two releases, or a rerun after an upload,
# get distinct increasing codes unless they start in the same second.
# Minutes, used before, repeated for two releases in one minute and the
# second upload failed. This epoch keeps every code above any the minute
# scheme produced (about 29.8 million in 2026) and below Play's documented
# 2,100,000,000 ceiling until about 2090.
now="${REDLINE_RELEASE_NOW:-$(date -u +%s)}"
# At most ten digits: anything longer is far past Play's ceiling anyway, and
# would overflow bash arithmetic into a code that passes the range check.
if [[ ! "${now}" =~ ^[0-9]{1,10}$ ]]; then
  printf 'REDLINE_RELEASE_NOW %q is not a Unix time\n' "${now}" >&2
  exit 1
fi
code=$(( now - 1700000000 ))
if (( code < 1 || code > 2100000000 )); then
  printf 'derived versionCode %s is outside Play'\''s valid range (1-2100000000)\n' "${code}" >&2
  exit 1
fi

printf 'name=%s\ncode=%s\n' "${version}" "${code}"
