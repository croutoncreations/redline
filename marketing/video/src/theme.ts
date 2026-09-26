import { loadFont as loadInter } from '@remotion/google-fonts/Inter';
import { loadFont as loadMono } from '@remotion/google-fonts/JetBrainsMono';

// Matches internal/api/dashboard/dashboard.css so the motion graphics and the
// real UI captures read as one product.
export const color = {
  bg: '#0d0f12',
  panel: '#15191d',
  panel2: '#1a1f24',
  line: '#2a3036',
  text: '#f2f0ed',
  muted: '#91989e',
  accent: '#f0524d',
  accentSoft: '#3b2021',
  amber: '#efbd62',
  green: '#72d9a4',
  logoRed: '#ef3f45',
  logoWhite: '#f7f7f8',
};

export const font = {
  sans: loadInter('normal', { weights: ['400', '500', '600', '700', '800'], subsets: ['latin'] }).fontFamily,
  mono: loadMono('normal', { weights: ['400', '500', '700'], subsets: ['latin'] }).fontFamily,
};

export const FPS = 30;

// "Launch Day Loop" runs at 97.5 BPM with its first downbeat at 0.406s. The
// soundtrack is trimmed by that offset so beat 0 is frame 0 and every scene
// boundary below lands on the grid.
export const BPM = 97.5;
export const AUDIO_OFFSET_FRAMES = Math.round(0.406 * FPS);
export const BEAT = (FPS * 60) / BPM;
export const beats = (n: number) => Math.round(n * BEAT);
export const bars = (n: number) => beats(n * 4);
