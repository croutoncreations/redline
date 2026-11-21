// Renders each mockup frame in prototype.html to a PNG at Pixel 9 density.
// Usage: node docs/design/multi-account-usage/render.cjs [outDir]
// Local file only; makes no network requests.
// Renders to .redline/renders (git-ignored) by default, never into .artifacts/. Published DevX
// copies are stored by basename, so a revision must be registered under a NEW filename
// (e.g. A1-overview-r2.png) and must never replace or remove a published artifact.
const path = require('path');
const fs = require('fs');
const { chromium } = require('playwright');

(async () => {
  // `--page selected-a-r3.html` renders the r3 page; its comparison strip is saved as A3-carousel-strip-r3.png.
  const pageArg = process.argv.indexOf('--page');
  const pageFile = pageArg > -1 ? process.argv[pageArg + 1] : 'prototype.html';
  const positional = process.argv.slice(2).filter((a, i, all) => a !== '--page' && all[i - 1] !== '--page');
  const outDir = path.resolve(positional[0] || '.redline/renders');
  fs.mkdirSync(outDir, { recursive: true });
  const browser = await chromium.launch();
  // Pixel 9: 1080x2424 px at 2.625 dpr = 412x923 dp.
  const page = await browser.newPage({ viewport: { width: 2000, height: 1200 }, deviceScaleFactor: 2.625 });
  await page.route(/^https?:/, (route) => route.abort());
  await page.goto('file://' + path.join(__dirname, pageFile));
  const ids = await page.evaluate(() => window.FRAMES);
  for (const id of ids) {
    await page.locator(`[id="${id}"]`).screenshot({ path: path.join(outDir, id + '.png') });
  }
  await page.locator('#comparison').screenshot({ path: path.join(outDir, pageFile === 'prototype.html' ? 'ABC-comparison-r2.png' : 'A3-carousel-strip-r3.png'), scale: 'device' });
  // Flag any text that overflows its box (clipping check).
  const clipped = await page.evaluate(() => [...document.querySelectorAll('.phone *')]
    .filter((el) => el.children.length === 0 && el.textContent.trim() && el.scrollWidth > el.clientWidth + 1
      && getComputedStyle(el).overflow !== 'visible')
    .map((el) => el.textContent.trim().slice(0, 60)));
  console.log(JSON.stringify({ frames: ids, outDir, clipped }, null, 2));
  await browser.close();
})();
