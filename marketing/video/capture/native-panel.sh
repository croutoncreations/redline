#!/usr/bin/env bash
# Captures the native menu-bar quick panel against the `running` demo scene.
# macOS only; needs Screen Recording permission for the terminal. Writes
# public/captures/native-quick-panel.png at the display's backing scale (2x on
# Retina).
#
#   ./capture/native-panel.sh            # builds dist/Redline.app if missing
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"
out="$here/../public/captures/native-quick-panel.png"
port="${PROMO_NATIVE_PORT:-7491}"
app="$repo/dist/Redline.app/Contents/MacOS/RedlineMenuBar"
bin="${REDLINE_BIN:-$(mktemp -d)/redline}"

[[ -x "$bin" ]] || (cd "$repo" && go build -o "$bin" ./cmd/redline)
# UNUserNotificationCenter needs a real bundle, so a bare `swift build` binary won't do.
[[ -x "$app" ]] || (cd "$repo" && REDLINE_SIGN_IDENTITY=- ./scripts/build-macos-app.sh)
if curl -s -o /dev/null "http://127.0.0.1:$port"; then echo "port $port is busy" >&2; exit 1; fi

state="$(mktemp -d)/demo"
"$bin" demo serve --scenario running --listen "127.0.0.1:$port" --state-dir "$state" >/dev/null &
demo=$!
# The builder-updates banner and DEMO badge are product UI, not marketing UI:
# dismiss the banner via its defaults key; mask the badge after capture.
REDLINE_API_URL="http://127.0.0.1:$port" REDLINE_CONFIG_PATH="$state/config.yaml" \
  "$app" --show-popover-preview --suppress-first-run-ui -builder-updates-prompt-dismissed YES >/dev/null 2>&1 &
panel=$!
trap 'kill $panel $demo 2>/dev/null || true' EXIT
sleep 6

window="$(swift - "$panel" <<'SWIFT'
import CoreGraphics
let pid = Int32(CommandLine.arguments[1])!
let list = CGWindowListCopyWindowInfo([.optionOnScreenOnly], kCGNullWindowID) as! [[String: Any]]
for w in list where (w[kCGWindowOwnerPID as String] as? Int32) == pid && (w[kCGWindowName as String] as? String ?? "").contains("Preview") {
    print(w[kCGWindowNumber as String]!)
}
SWIFT
)"
[[ -n "$window" ]] || { echo "quick panel preview window not found" >&2; exit 1; }
raw="$(mktemp).png"
screencapture -o -x -l "$window" "$raw"
# Crop the title bar (58px @2x) and paint over the DEMO badge with the header color.
ffmpeg -v error -y -i "$raw" -vf "drawbox=x=428:y=86:w=176:h=38:color=0x2d3136:t=fill,crop=iw:ih-58:0:58" -update 1 "$out"
echo "wrote $out"
