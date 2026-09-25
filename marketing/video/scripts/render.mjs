// Renders every deliverable into out/.
//
//   npm run render                 # all cuts
//   npm run render -- claude       # one provider
//   npm run render -- claude wide  # one provider, one format
//
// Produces per provider:
//   redline-<p>-youtube-1080p.mp4   16:9, H.264, music
//   redline-<p>-square.mp4          1:1,  H.264, music (X / Reddit / LinkedIn)
//   redline-<p>-loop.gif            silent loop, 960px, gifski
//   redline-<p>-loop.mp4            same loop as MP4 (X converts GIFs anyway)
import { execFileSync } from 'node:child_process';
import { mkdirSync, rmSync, statSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const out = join(root, 'out');
mkdirSync(out, { recursive: true });

const [onlyProvider, onlyFormat] = process.argv.slice(2);
const providers = [
  { id: 'claude', label: 'Claude' },
  { id: 'codex', label: 'Codex' },
].filter(p => !onlyProvider || p.id === onlyProvider);

const run = (cmd, args) => execFileSync(cmd, args, { cwd: root, stdio: 'inherit' });
const remotion = (composition, file, extra = []) =>
  run('npx', ['remotion', 'render', 'src/index.ts', composition, file, '--codec=h264', '--crf=18', '--pixel-format=yuv420p', '--concurrency=50%', ...extra]);
const size = f => `${(statSync(f).size / 1e6).toFixed(1)} MB`;

for (const p of providers) {
  if (!onlyFormat || onlyFormat === 'wide') {
    const f = join(out, `redline-${p.id}-youtube-1080p.mp4`);
    remotion(`Promo${p.label}Wide`, f);
    console.log(`✓ ${f} (${size(f)})`);
  }
  if (!onlyFormat || onlyFormat === 'square') {
    const f = join(out, `redline-${p.id}-square.mp4`);
    remotion(`Promo${p.label}Square`, f);
    console.log(`✓ ${f} (${size(f)})`);
  }
  if (!onlyFormat || onlyFormat === 'gif') {
    const mp4 = join(out, `redline-${p.id}-loop.mp4`);
    remotion(`Loop${p.label}Wide`, mp4, ['--muted']);
    // gifski from extracted frames gives far better gradients than ffmpeg's palette.
    const frames = join(out, `.frames-${p.id}`);
    rmSync(frames, { recursive: true, force: true });
    mkdirSync(frames);
    run('ffmpeg', ['-v', 'error', '-i', mp4, '-vf', 'fps=20,scale=960:-1:flags=lanczos', join(frames, 'f%04d.png')]);
    const gif = join(out, `redline-${p.id}-loop.gif`);
    run('sh', ['-c', `gifski --quiet --fps 20 --width 960 --quality 85 -o "${gif}" "${frames}"/f*.png`]);
    rmSync(frames, { recursive: true, force: true });
    console.log(`✓ ${mp4} (${size(mp4)})\n✓ ${gif} (${size(gif)})`);
  }
}
