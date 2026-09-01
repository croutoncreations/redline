#!/usr/bin/env bash
# Regenerate AppIcon.icns from AppIcon.svg.
#
# build-macos-app.sh copies the committed AppIcon.icns straight into the bundle,
# so the .icns is the artifact that actually ships and the .svg is its source.
# Run this after editing the SVG, otherwise the two silently drift apart.
#
# Requires librsvg: brew install librsvg
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
resources="${repository_root}/macos/Sources/RedlineMenuBar/Resources"
svg="${resources}/AppIcon.svg"
icns="${resources}/AppIcon.icns"

if ! command -v rsvg-convert >/dev/null 2>&1; then
  echo "rsvg-convert not found. Install it with: brew install librsvg" >&2
  exit 1
fi

workdir="$(mktemp -d)"
trap 'rm -rf "${workdir}"' EXIT
iconset="${workdir}/AppIcon.iconset"
mkdir -p "${iconset}"

# size:name pairs required by iconutil for a complete iconset.
for spec in \
  16:icon_16x16 \
  32:icon_16x16@2x \
  32:icon_32x32 \
  64:icon_32x32@2x \
  128:icon_128x128 \
  256:icon_128x128@2x \
  256:icon_256x256 \
  512:icon_256x256@2x \
  512:icon_512x512 \
  1024:icon_512x512@2x
do
  size="${spec%%:*}"
  name="${spec##*:}"
  rsvg-convert -w "${size}" -h "${size}" "${svg}" -o "${iconset}/${name}.png"
done

iconutil -c icns "${iconset}" -o "${icns}"
echo "Regenerated ${icns} from $(basename "${svg}")"
