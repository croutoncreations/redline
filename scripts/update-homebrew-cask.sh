#!/usr/bin/env bash
# Render Casks/redline.rb for croutoncreations/homebrew-tap from a released
# DMG and push it. Run after scripts/package-macos-release.sh has produced the
# notarized DMG and it has been uploaded to the GitHub release for the tag.
#
# Usage:
#   REDLINE_VERSION=1.2.0 scripts/update-homebrew-cask.sh dist/release/Redline-1.2.0-universal.dmg
#
# Environment:
#   REDLINE_VERSION       required; must match the release tag without the leading v
#   HOMEBREW_TAP_TOKEN    optional; a token with contents:write on the tap. When
#                         unset the script uses whatever credentials git/gh already have.
#   REDLINE_CASK_DRY_RUN  set to 1 to print the rendered cask and skip the push.
set -euo pipefail

dmg_path="${1:-}"
version="${REDLINE_VERSION:-}"
tap_repo="croutoncreations/homebrew-tap"
release_repo="croutoncreations/redline"

if [[ -z "${dmg_path}" || ! -s "${dmg_path}" ]]; then
  printf 'usage: REDLINE_VERSION=x.y.z %s path/to/Redline-x.y.z-universal.dmg\n' "$0" >&2
  exit 1
fi
if [[ ! "${version}" =~ ^[0-9]+\.[0-9]+(\.[0-9]+)?$ ]]; then
  printf 'REDLINE_VERSION must contain two or three numeric components (for example 1.2.0).\n' >&2
  exit 1
fi

dmg_name="$(basename "${dmg_path}")"
if [[ "${dmg_name}" != "Redline-${version}-"*.dmg ]]; then
  printf 'DMG name %s does not match REDLINE_VERSION=%s\n' "${dmg_name}" "${version}" >&2
  exit 1
fi
sha256="$(shasum -a 256 "${dmg_path}" | awk '{print $1}')"

cask="$(cat <<EOF
cask "redline" do
  version "${version}"
  sha256 "${sha256}"

  url "https://github.com/${release_repo}/releases/download/v#{version}/${dmg_name}"
  name "Redline"
  desc "Budget-aware dispatcher that spends spare Codex and Claude subscription quota on queued agent jobs"
  homepage "https://github.com/${release_repo}"

  livecheck do
    url :url
    strategy :github_latest
  end

  depends_on macos: ">= :ventura"
  conflicts_with formula: "redline"

  app "Redline.app"
  binary "#{appdir}/Redline.app/Contents/Resources/bin/redline"

  uninstall quit: "ai.redline.mac"

  zap trash: [
    "~/Library/Application Support/Redline",
    "~/Library/Logs/Redline",
    "~/Library/Preferences/ai.redline.mac.plist",
  ]

  caveats <<~EOS
    Redline installs a signed, notarized menu-bar app. Automatic dispatch is
    off until you enable it in the app.

    A standalone CLI formula is also available:
      brew install croutoncreations/tap/redline
  EOS
end
EOF
)"

if [[ "${REDLINE_CASK_DRY_RUN:-0}" == "1" ]]; then
  printf '%s\n' "${cask}"
  exit 0
fi

work="$(mktemp -d "${TMPDIR:-/tmp}/redline-cask.XXXXXX")"
trap 'rm -rf "${work}"' EXIT

clone_url="https://github.com/${tap_repo}.git"
if [[ -n "${HOMEBREW_TAP_TOKEN:-}" ]]; then
  clone_url="https://x-access-token:${HOMEBREW_TAP_TOKEN}@github.com/${tap_repo}.git"
fi
git clone --quiet --depth 1 "${clone_url}" "${work}/tap"
mkdir -p "${work}/tap/Casks"
printf '%s\n' "${cask}" > "${work}/tap/Casks/redline.rb"

cd "${work}/tap"
git add Casks/redline.rb
if git diff --cached --quiet; then
  printf 'Cask already at %s; nothing to push.\n' "${version}"
  exit 0
fi
git -c user.name=redline-release-bot -c user.email=redline@croutoncreations.com \
  commit --quiet -m "redline cask v${version}"
git push --quiet origin HEAD:main
printf 'Pushed Casks/redline.rb for v%s to %s\n' "${version}" "${tap_repo}"
