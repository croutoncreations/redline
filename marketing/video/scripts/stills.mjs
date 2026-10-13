// Contact sheet for quick review: one bundle, many frames.
//   node scripts/stills.mjs PromoClaudeWide 60 300 900
import { bundle } from '@remotion/bundler';
import { openBrowser, renderStill, selectComposition } from '@remotion/renderer';
import { mkdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const [id, ...frames] = process.argv.slice(2);
const outDir = process.env.STILLS_DIR || join(root, 'out', 'stills');
mkdirSync(outDir, { recursive: true });
const serveUrl = await bundle({ entryPoint: join(root, 'src/index.ts') });
const browser = await openBrowser('chrome');
const composition = await selectComposition({ serveUrl, id, puppeteerInstance: browser });
for (const frame of frames.map(Number)) {
  const output = join(outDir, `${id}-${String(frame).padStart(4, '0')}.png`);
  await renderStill({ composition, serveUrl, frame, output, puppeteerInstance: browser });
  console.log(output);
}
await browser.close({ silent: true });
