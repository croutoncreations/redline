#!/usr/bin/env bash
# Fail when the Homebrew tap lags the latest published (non-pre-release)
# GitHub release. The formula is pushed by GoReleaser; the cask is pushed by
# scripts/update-homebrew-cask.sh after the DMG is built on a Mac, so the cask
# is allowed REDLINE_CASK_GRACE_HOURS (default 48) to catch up.
#
# Usage: scripts/check-homebrew-versions.sh
# Needs: gh (authenticated, or GH_TOKEN), jq.
set -euo pipefail

release_repo="${REDLINE_RELEASE_REPO:-croutoncreations/redline}"
tap_repo="${REDLINE_TAP_REPO:-croutoncreations/homebrew-tap}"
grace_hours="${REDLINE_CASK_GRACE_HOURS:-48}"

# `gh release view` with no tag returns the latest non-draft, non-pre-release.
release="$(gh release view --repo "${release_repo}" --json tagName,publishedAt)"
tag="$(jq -r '.tagName // empty' <<<"${release}")"
published="$(jq -r '.publishedAt // empty' <<<"${release}")"
if [[ -z "${tag}" || -z "${published}" ]]; then
  printf 'check-homebrew-versions: could not read the latest release of %s\n' "${release_repo}" >&2
  exit 2
fi
want="${tag#v}"
age_hours=$(( ( $(date -u +%s) - $(jq -rn --arg t "${published}" '$t | fromdateiso8601') ) / 3600 ))

tap_version() {
  local content version
  content="$(gh api "repos/${tap_repo}/contents/$1" --jq .content | base64 --decode)"
  version="$(sed -nE 's/^  version "([^"]+)".*/\1/p' <<<"${content}" | head -n 1)"
  if [[ -z "${version}" ]]; then
    printf 'check-homebrew-versions: no version line found in %s/%s\n' "${tap_repo}" "$1" >&2
    exit 2
  fi
  printf '%s' "${version}"
}
formula="$(tap_version Formula/redline.rb)"
cask="$(tap_version Casks/redline.rb)"

printf 'Latest release: %s (published %sh ago)\nFormula:        %s\nCask:           %s\n' \
  "${tag}" "${age_hours}" "${formula}" "${cask}"

status=0
if [[ "${formula}" != "${want}" ]]; then
  printf '::error::Formula/redline.rb is %s but the latest release is %s. Check the Release workflow run for %s.\n' \
    "${formula}" "${want}" "${tag}"
  status=1
fi
if [[ "${cask}" != "${want}" ]]; then
  if (( age_hours >= grace_hours )); then
    printf '::error::Casks/redline.rb is %s but %s was released %sh ago. Upload the DMG and run scripts/update-homebrew-cask.sh (docs/releasing.md, step 4).\n' \
      "${cask}" "${tag}" "${age_hours}"
    status=1
  else
    printf '::warning::Casks/redline.rb is %s; %s is %sh old (grace %sh). Run scripts/update-homebrew-cask.sh once the DMG is uploaded.\n' \
      "${cask}" "${tag}" "${age_hours}" "${grace_hours}"
  fi
fi
exit "${status}"
