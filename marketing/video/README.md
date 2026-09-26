# Redline promo video

Motion-graphics promo built with [Remotion](https://remotion.dev). The video is
code, so copy, timing, and captures can be changed and re-rendered.

## Deliverables

`npm run render` writes to `out/` (git-ignored):

| File | Use |
|---|---|
| `redline-<provider>-youtube-1080p.mp4` | YouTube, 16:9, ~62s, music |
| `redline-<provider>-square.mp4` | X / Reddit / LinkedIn feeds, 1:1, music |
| `redline-<provider>-loop.gif` | Silent ~11s loop for X, Reddit, READMEs, Discord |
| `redline-<provider>-loop.mp4` | Same loop as MP4 (X turns GIFs into video anyway) |

There are two cuts, `claude` and `codex`. Each is written for its own community
and mentions the other supported agents near the end.

## Workflow

```bash
npm install
npm run capture   # re-shoot dashboard captures from `redline demo serve` (needs Go)
./capture/native-panel.sh   # re-shoot the native menu-bar quick panel (macOS, Screen Recording permission)
npm run studio    # live preview/scrub in the browser
npm run render    # all cuts; or: npm run render -- codex square
node scripts/stills.mjs PromoClaudeWide 300 900   # quick review frames → out/stills
```

## Structure

- `capture/capture.mjs` starts the isolated demo service and captures the
  dashboard at 3200×1800. It also writes `public/captures/manifest.json`,
  which holds the element boxes the camera zooms to. The DEMO pill is hidden
  only in these marketing captures.
- `src/theme.ts` holds the colors and fonts from the dashboard, plus the beat
  grid. "Launch Day Loop" is 97.5 BPM with its first downbeat at 0.406s, and
  every scene is a whole number of bars.
- `src/providers.ts` holds the per-cut copy and the pace numbers. They match
  the `decision-run` demo scene, so the explainer agrees with the real UI.
- `src/scenes/` is the storyboard, in order: Hook → Problem → Logo → Queue →
  Pace → Product → Anywhere (terminal, agent over MCP, menu bar) → Payoff →
  Outro. The Anywhere copy mirrors shipped behavior: the `redline later`
  output is verbatim and the MCP tool names are real.
- `src/Loop.tsx` is the GIF: the Pace explainer ending on RUN, then a brand
  card.

## Music

`public/audio/launch-day-loop.mp3` is "Launch Day Loop" from Jon's
Downloads folder. Confirm the license covers YouTube and social use before
publishing.
