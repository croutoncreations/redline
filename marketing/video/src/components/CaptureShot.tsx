import React from 'react';
import { AbsoluteFill, Img, interpolate, staticFile, useCurrentFrame } from 'remotion';
import manifest from '../../public/captures/manifest.json';
import { color } from '../theme';
import { easeInOut, progress, useLayout } from './primitives';

// A camera over one real dashboard capture. Boxes come from manifest.json
// (written by capture/capture.mjs), so zooms track the actual layout.

type Box = { x: number; y: number; width: number; height: number };
type Shots = Record<string, Record<string, Box>>;
const shots = manifest.shots as Shots;
const IMAGE = manifest.viewport; // CSS px; the PNGs are 2x this

export type CameraKey = { at: number; box: string; pad?: number; duration?: number };
export type Ring = { box: string; at: number; pad?: number };

const MAX_SCALE = 2.3; // captures are 2x, so this stays close to native sharpness

function boxFor(shot: string, key: string): Box {
  const box = shots[shot]?.[key];
  if (!box) throw new Error(`capture ${shot} has no box "${key}"; re-run npm run capture`);
  return box;
}

export const CaptureShot: React.FC<{
  shot: string;
  keys: CameraKey[];
  rings?: Ring[];
  children?: React.ReactNode;
}> = ({ shot, keys, rings = [], children }) => {
  const frame = useCurrentFrame();
  const { width: W, height: H, u } = useLayout();
  const cover = Math.max(W / IMAGE.width, H / IMAGE.height);

  const target = (k: CameraKey) => {
    const b = boxFor(shot, k.box);
    const pad = (k.pad ?? 32) ;
    const s = Math.min(MAX_SCALE, Math.max(cover, Math.min(W / (b.width + pad * 2), H / (b.height + pad * 2))));
    return { cx: b.x + b.width / 2, cy: b.y + b.height / 2, ls: Math.log(s) };
  };

  let cam = target(keys[0]);
  for (const k of keys.slice(1)) {
    const t = progress(frame, k.at, k.duration ?? 26, easeInOut);
    const next = target(k);
    cam = { cx: cam.cx + (next.cx - cam.cx) * t, cy: cam.cy + (next.cy - cam.cy) * t, ls: cam.ls + (next.ls - cam.ls) * t };
  }
  // Slow push-in keeps held shots alive.
  const s = Math.exp(cam.ls) * interpolate(frame, [0, 240], [1, 1.035]);
  const tx = Math.min(0, Math.max(W - s * IMAGE.width, W / 2 - s * cam.cx));
  const ty = Math.min(0, Math.max(H - s * IMAGE.height, H / 2 - s * cam.cy));
  const project = (b: Box, pad = 0) => ({ left: tx + (b.x - pad) * s, top: ty + (b.y - pad) * s, width: (b.width + pad * 2) * s, height: (b.height + pad * 2) * s });

  return (
    <AbsoluteFill style={{ overflow: 'hidden', background: color.bg }}>
      <Img
        src={staticFile(`captures/${shot}.png`)}
        style={{ position: 'absolute', left: 0, top: 0, width: IMAGE.width, height: IMAGE.height, transformOrigin: '0 0', transform: `translate(${tx}px, ${ty}px) scale(${s})` }}
      />
      {rings.map((ring, i) => {
        const p = progress(frame, ring.at, 18);
        if (p <= 0) return null;
        const r = project(boxFor(shot, ring.box), (ring.pad ?? 8) * (1 + (1 - p) * 0.6));
        return (
          <div
            key={i}
            style={{
              position: 'absolute',
              ...r,
              borderRadius: 14 * u,
              border: `${3 * u}px solid ${color.accent}`,
              boxShadow: `0 0 ${36 * u}px rgba(240,82,77,${0.55 * p}), inset 0 0 ${24 * u}px rgba(240,82,77,${0.18 * p})`,
              opacity: p,
            }}
          />
        );
      })}
      <AbsoluteFill style={{ background: 'radial-gradient(ellipse at center, transparent 55%, rgba(0,0,0,.55) 100%)' }} />
      {children}
    </AbsoluteFill>
  );
};

/** Lower-third caption over a capture. */
export const Caption: React.FC<{ title: string; sub?: string; at: number }> = ({ title, sub, at }) => {
  const frame = useCurrentFrame();
  const { u, square } = useLayout();
  const p = progress(frame, at, 18);
  const q = progress(frame, at + 8, 18);
  return (
    <AbsoluteFill style={{ justifyContent: 'flex-end', pointerEvents: 'none' }}>
      <div style={{ height: '46%', background: 'linear-gradient(transparent, rgba(8,9,11,.92) 70%)', position: 'absolute', left: 0, right: 0, bottom: 0, opacity: p }} />
      <div style={{ position: 'relative', padding: `0 ${square ? 64 * u : 110 * u}px ${square ? 72 * u : 84 * u}px` }}>
        <div style={{ fontFamily: 'inherit', display: 'flex', alignItems: 'center', gap: 18 * u, opacity: p, transform: `translateY(${(1 - p) * 24 * u}px)` }}>
          <div style={{ width: 8 * u, height: 58 * u, borderRadius: 4 * u, background: color.accent, boxShadow: `0 0 ${24 * u}px ${color.accent}` }} />
          <div>
            <div style={{ fontSize: (square ? 50 : 56) * u, fontWeight: 750, letterSpacing: '-0.025em', color: color.text }}>{title}</div>
            {sub && <div style={{ fontSize: 28 * u, color: color.muted, marginTop: 6 * u, opacity: q }}>{sub}</div>}
          </div>
        </div>
      </div>
    </AbsoluteFill>
  );
};
