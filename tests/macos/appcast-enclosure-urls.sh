#!/usr/bin/env bash
# generate_appcast gives every indexed DMG the current release's download
# prefix. With per-release GitHub URLs that pointed 0.1.7's full archive at
# the v0.1.9 release, where it does not exist. generate-sparkle-appcast.sh must
# repoint older DMGs at their own tag while leaving this release's DMG and
# deltas alone -- including a pre-release, whose DMG is named for the final
# version but uploaded only to the rc tag. Uses a stub generator, so it needs
# no Sparkle build or keys.
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/redline-appcast-urls.XXXXXX")"
trap 'rm -rf "${test_root}"' EXIT
root="https://github.com/croutoncreations/redline/releases/download"

# check_release TAG: generate an appcast for release TAG (DMG version 0.2.0)
# with older 0.1.9 and 0.1.7 DMGs, and check every enclosure URL.
check_release() {
  local tag="$1"
  local prefix="${root}/${tag}/"
  local case_root="${test_root}/${tag}"
  mkdir -p "${case_root}/release"
  touch "${case_root}/release/Redline-0.2.0-universal.dmg"
  cat >"${case_root}/generated.xml" <<EOF
<?xml version="1.0" standalone="yes"?>
<rss xmlns:sparkle="http://www.andymatuschak.org/xml-namespaces/sparkle" version="2.0">
    <channel>
        <item>
            <sparkle:version>12</sparkle:version>
            <enclosure url="${prefix}Redline-0.2.0-universal.dmg" length="1" type="application/octet-stream" sparkle:edSignature="new"/>
            <sparkle:deltas>
                <enclosure url="${prefix}Redline12-11.delta" sparkle:deltaFrom="11" length="1" type="application/octet-stream" sparkle:edSignature="delta"/>
            </sparkle:deltas>
        </item>
        <item>
            <sparkle:version>11</sparkle:version>
            <enclosure url="${prefix}Redline-0.1.9-universal.dmg" length="1" type="application/octet-stream" sparkle:edSignature="old"/>
        </item>
        <item>
            <sparkle:version>10</sparkle:version>
            <enclosure url="${prefix}Redline-0.1.7-universal.dmg" length="1" type="application/octet-stream" sparkle:edSignature="older"/>
        </item>
    </channel>
</rss>
EOF
  cat >"${case_root}/generate_appcast" <<EOF
#!/usr/bin/env bash
while [[ \$# -gt 0 ]]; do
  if [[ "\$1" == "-o" ]]; then cp "${case_root}/generated.xml" "\$2"; shift; fi
  shift
done
EOF
  chmod +x "${case_root}/generate_appcast"

  REDLINE_RELEASE_OUTPUT_DIR="${case_root}/release" \
  REDLINE_SPARKLE_FEED_URL="https://github.com/croutoncreations/redline/releases/latest/download/appcast.xml" \
  REDLINE_SPARKLE_DOWNLOAD_URL_PREFIX="${prefix}" \
  REDLINE_SPARKLE_GENERATE_APPCAST="${case_root}/generate_appcast" \
    "${repository_root}/scripts/generate-sparkle-appcast.sh" >/dev/null

  local appcast="${case_root}/release/appcast.xml"
  local url
  for url in \
    "${prefix}Redline-0.2.0-universal.dmg" \
    "${prefix}Redline12-11.delta" \
    "${root}/v0.1.9/Redline-0.1.9-universal.dmg" \
    "${root}/v0.1.7/Redline-0.1.7-universal.dmg"; do
    if ! grep -Fq "url=\"${url}\"" "${appcast}"; then
      printf '%s: expected enclosure %s in:\n' "${tag}" "${url}" >&2
      cat "${appcast}" >&2
      exit 1
    fi
  done
  xmllint --noout "${appcast}"
}

check_release v0.2.0
# docs/releasing.md: a pre-release builds REDLINE_VERSION=0.2.0 and points the
# download prefix at the rc tag, the only release that DMG is uploaded to.
check_release v0.2.0-rc.1

printf 'appcast enclosure URL test passed\n'
