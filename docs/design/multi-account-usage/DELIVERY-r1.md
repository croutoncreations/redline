# Multi-account usage mockups: delivery manifest (r1)

**Frozen delivery set.** Do not re-render, re-register, rename or remove these artifacts. A later revision gets
new filenames (`*-r2.png`, `DESIGN-r2.md`) and new artifact IDs, and the r1 entries below stay published.
Every artifact below has **archive** retention. All data is synthetic, and each screenshot is stamped
`OPTION X · MOCKUP · SYNTHETIC DATA`.

Session: `redline-multi-account-opus55-oct10`. Published path prefix: `/sessions/redline-multi-account-opus55-oct10/artifacts/`.
Checksums are SHA-256 of the published copies in `.artifacts/screenshots/`. I verified them unchanged after the retention change.

| Artifact ID | Published file | Size (px) | SHA-256 |
|---|---|---|---|
| `screenshot-a-b-c-comparison-sheet-pick-one-synthetic-data-20261010061423-763690000` | `screenshots/ABC-comparison.png` | 2016×3528 | `fe270ce8c020e19aab5e1b2053247ef85571165e0e5b6817867b9e558c1cb3ae` |
| `screenshot-option-a-recommended-summary-tile-carousel-overview-20261010061423-801226000` | `screenshots/A1-overview.png` | 1082×2423 | `69cd66ff70c3e7f2a0517fca9951736067faf43080d2c4a6b43abbee3c40ca8c` |
| `screenshot-option-a-swiped-to-account-2-of-3-used-up-20261010061423-817924000` | `screenshots/A2-switch-account.png` | 1082×2423 | `b6b80ae7cf615d49a0129287248f6000397ce950f6bc040634099b8562025ab5` |
| `screenshot-option-b-chip-switcher-in-one-card-overview-20261010061423-827734000` | `screenshots/B1-overview.png` | 1082×2423 | `39ab7c675f10568a47ac03ee484c6b270d3d6b1378bcdac9207f469f1636706d` |
| `screenshot-option-b-second-chip-selected-20261010061423-837903000` | `screenshots/B2-switch-account.png` | 1082×2423 | `08289a1e3be130c71aea82faacef808ba0c09c095aff34d49859c1ae8899a3b4` |
| `screenshot-option-c-summary-only-usage-tab-20261010061423-847531000` | `screenshots/C1-overview.png` | 1082×2423 | `8e315cddcb3821025fe4f7c42e62f652eaf64357b03e0755eb8189ec88e135da` |
| `screenshot-option-c-full-screen-detail-account-2-of-3-20261010061423-857242000` | `screenshots/C2-switch-account.png` | 1082×2423 | `f574e4326c6385da59f5de48d46dcb576c5849d91e3467d9434c0db63b3b5fdd` |

Supporting frames, which are the same for every option:

| Artifact ID | Published file | SHA-256 |
|---|---|---|
| `screenshot-05-states-stale-sign-in-unmetered-paused-monitor-only-20261010061056-112162000` | `screenshots/05-states.png` | `7a0a16d6e3d161b3cc2aeed1d0c7476ac28e8f3637d8c9a706c8224b18250d09` |
| `screenshot-06-one-account-backward-compatible-20261010061056-122963000` | `screenshots/06-one-account.png` | `4c54eef60e9f56a1ea04b0cecbbdea1eab7557ce3e1bbac8bbaae8d39764186e` |
| `screenshot-07-many-accounts-7-20261010061056-133163000` | `screenshots/07-many-accounts.png` | `3d5f25bd95c0d1b2c6703a3571d6ac002e2965bdf85ecef161ac5045ac59df0d` |
| `screenshot-08-account-chooser-sheet-tap-alternative-20261010061056-142515000` | `screenshots/08-account-chooser.png` | `5b8b5404f634e555e89715c17b898ba240381ea260020b787e1030766a72e444` |
| `screenshot-09-queue-tab-per-account-routing-20261010061056-152455000` | `screenshots/09-queue-per-account.png` | `dff30ffa3bbbc6e6ffba168701601cc2cf8e4a7b3765594aa01ab4239606582b` |
| `screenshot-00-today-s-android-claude-card-reference-20261010061056-162335000` | `screenshots/00-baseline-current.png` | `347f1c920d6cb34a6e1c0ea806213f005c1f4274952e8cc6b4bc5bfe699187c2` |

Documents (archive retention):
- `plan-multi-account-usage-design-plan-openusage-evidence-update-20261010061928-371635000`: `DESIGN.md` as of commit `ed1a83f`
- `document-html-prototype-source-synthetic-data-20261010061435-006613000`: `prototype.html`

## Option labels and trade-offs
- **A · Summary tile and carousel (recommended).** Overview and detail sit on one screen; tap a row, swipe, or use ‹ ›. Best for comparing accounts at a glance, but it is the tallest layout.
- **B · Chip switcher in one card.** Chips switch the card body. It is the most compact and closest to today's card, but it gives the weakest overview and gets crowded past about 4 accounts.
- **C · Summary tab and full-screen detail.** Tapping a row opens a full-screen pager with a back arrow. It scales best to many accounts, but adds one navigation level and removes the usage bars from the tab.

## Withdrawn artifact IDs (removed before this freeze; do not request)
`screenshot-01-variant-a-…-052757000`, `screenshot-02-…-080899000`, `screenshot-03-…-091949000`,
`screenshot-04-…-102323000`, `plan-…-014398000`, `plan-…-20261010061434-996123000`,
`document-interactive-html-prototype-source-…-171911000`. I replaced these before the freeze and won't remove anything else.
