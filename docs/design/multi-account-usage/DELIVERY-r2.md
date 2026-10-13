# Multi-account usage mockups: delivery manifest r2 (Option B corrected)

This is an **addendum to r1, not a replacement.** All r1 artifacts (`DELIVERY-r1.md`) stay published, unchanged and on
archive retention. After this registration I verified the SHA-256 of all 13 r1 PNGs again, and every one matched.
All data is synthetic, and every r2 frame is stamped `OPTION X r2 · MOCKUP · SYNTHETIC DATA`.

## What changed in r2 (mockup polish only)
1. **Option B status copy mismatch (reported by the reviewer).** The "Personal" account showed 41% weekly left alongside
   "Dispatching · weekly above the 50% floor", which contradicts itself.
   - **Fix:** under the standard policy the 50% floor arms at 72h before reset and the 20% floor at 24h. Personal's weekly
     resets in 3d 4h (76h), so **no floor is armed yet**. The line now reads *"Dispatching · no weekly floor armed yet · 50%
     floor arms in 4h"*, and both floor ticks on the bar are drawn faint (unarmed), matching the existing Android
     convention. The 41% figure itself is unchanged.
   - In the same pass I fixed the scenario data so that the "Research" account in the many-accounts frame (26h to reset,
     so the 50% floor is armed) reads *"weekly 12% left is below the armed 50% floor"*. That frame is not re-published.
2. **Long account names in Option B chips** wrapped to 4 lines (the "Client — Northwind long-running engagement" alias).
   - **Fix:** the chip alias is limited to **2 lines with an ellipsis** (`maxLines = 2, TextOverflow.Ellipsis`).
   - The **full name** is the chip's accessible label (TalkBack: *"Claude account 2 of 3, Client — Northwind long-running
     engagement, Team plan. 5-hour 0 percent left, weekly 58 percent left. Selected."*), it appears in a long-press
     tooltip, and it is always the heading of the card body. Nothing is hidden.
   - Users can type any alias length. The display is bounded, and settings may suggest at most 24 characters.

## New artifacts (archive retention), using only these for the corrected B
| Artifact ID | Published file | Size (px) | SHA-256 |
|---|---|---|---|
| `screenshot-r2-a-b-c-comparison-sheet-b-corrected-pick-one-20261010062617-877935000` | `screenshots/ABC-comparison-r2.png` | 2016×3528 | `1d905516a12e907db39d1877d4713cdd8db0a7bc29f5c5538b8278046c3f6bb2` |
| `screenshot-option-b-corrected-r2-overview-status-copy-fixed-2-line-chip-20261010062617-892986000` | `screenshots/B1-overview-r2.png` | 1082×2423 | `fd66da3c04877d82e6384bde6938c9338d8a8208317edb45131d9e48d17db0f3` |
| `screenshot-option-b-corrected-r2-second-chip-selected-20261010062617-906428000` | `screenshots/B2-switch-account-r2.png` | 1082×2423 | `58680bf2a6f148348a83f4bdb473510559583e372166b411b450372f365e521d` |
| `screenshot-option-b-corrected-r2-long-name-chip-talkback-label-20261010062617-919341000` | `screenshots/B-chip-a11y-r2.png` | ~1082 wide | `645b05bd2982ccf5c0316dc0f3ace5a0b1da47d4f88502844a7010f481099675` |

## Which version to show the owner
- **Option B:** use the r2 IDs above. r1 `B1-overview`/`B2-switch-account` are superseded but intentionally left published.
- **Options A and C:** r1 A1/A2/C1/C2 remain current. Their screens are visually identical in r2 apart from the badge
  text, so they were not re-published as individual files. The r2 comparison sheet shows them with an `r2` badge.
- **Comparison:** prefer `ABC-comparison-r2.png`; r1 `ABC-comparison.png` still contains the B copy error.
