#!/usr/bin/env bash
# generate_appcast gives every indexed DMG the current release's download
# prefix. With per-release GitHub URLs that pointed 0.1.7's full archive at
# the v0.1.9 release, where it does not exist. generate-sparkle-appcast.sh must
# repoint older DMGs at their own tag while leaving this release's DMG and
# deltas alone. Uses a stub generator, so it needs no Sparkle build or keys.
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/redline-appcast-urls.XXXXXX")"
trap 'rm -rf "${test_root}"' EXIT

prefix="https://github.com/croutoncreations/redline/releases/download/v0.2.0/"
mkdir -p "${test_root}/release"
touch "${test_root}/release/Redline-0.2.0-universal.dmg"
cat >"${test_root}/generated.xml" <<EOF
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
cat >"${test_root}/generate_appcast" <<EOF
#!/usr/bin/env bash
while [[ \$# -gt 0 ]]; do
  if [[ "\$1" == "-o" ]]; then cp "${test_root}/generated.xml" "\$2"; shift; fi
  shift
done
EOF
chmod +x "${test_root}/generate_appcast"

REDLINE_RELEASE_OUTPUT_DIR="${test_root}/release" \
REDLINE_SPARKLE_FEED_URL="https://github.com/croutoncreations/redline/releases/latest/download/appcast.xml" \
REDLINE_SPARKLE_DOWNLOAD_URL_PREFIX="${prefix}" \
REDLINE_SPARKLE_GENERATE_APPCAST="${test_root}/generate_appcast" \
  "${repository_root}/scripts/generate-sparkle-appcast.sh" >/dev/null

appcast="${test_root}/release/appcast.xml"
root="https://github.com/croutoncreations/redline/releases/download"
expect() {
  if ! grep -Fq "url=\"$1\"" "${appcast}"; then
    printf 'expected enclosure %s in:\n' "$1" >&2
    cat "${appcast}" >&2
    exit 1
  fi
}
expect "${root}/v0.2.0/Redline-0.2.0-universal.dmg"
expect "${root}/v0.2.0/Redline12-11.delta"
expect "${root}/v0.1.9/Redline-0.1.9-universal.dmg"
expect "${root}/v0.1.7/Redline-0.1.7-universal.dmg"
xmllint --noout "${appcast}"

printf 'appcast enclosure URL test passed\n'
