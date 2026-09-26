import React from 'react';
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import { Background, Mono, Words, progress, useLayout } from '../components/primitives';
import { beats, color } from '../theme';

// Week after week, the unused slice of each allowance burns off.
const weeks = [0.34, 0.52, 0.28, 0.45, 0.39, 0.3];

export const Problem: React.FC = () => {
  const frame = useCurrentFrame();
  const { u, square } = useLayout();
  const colH = (square ? 380 : 420) * u;
  const colW = (square ? 96 : 120) * u;
  return (
    <AbsoluteFill>
      <Background glow={0.7} />
      <AbsoluteFill style={{ alignItems: 'center', justifyContent: 'center', flexDirection: 'column', gap: 70 * u }}>
        <div style={{ display: 'flex', gap: (square ? 30 : 48) * u, alignItems: 'flex-end' }}>
          {weeks.map((unused, i) => {
            const start = i * beats(0.5);
            const rise = progress(frame, start, 18);
            const burn = progress(frame, start + beats(1.25), 22);
            return (
              <div key={i} style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 16 * u, opacity: rise, transform: `translateY(${(1 - rise) * 60 * u}px)` }}>
                <Mono size={20 * u} style={{ color: burn > 0.5 ? color.accent : color.muted, opacity: burn }}>
                  −{Math.round(unused * 100)}%
                </Mono>
                <div style={{ width: colW, height: colH, borderRadius: 16 * u, background: color.panel, border: `1px solid ${color.line}`, position: 'relative', overflow: 'hidden' }}>
                  <div style={{ position: 'absolute', left: 0, right: 0, bottom: 0, height: `${(1 - unused) * 100}%`, background: `linear-gradient(${color.muted}, #5f656b)`, opacity: 0.6 }} />
                  <div
                    style={{
                      position: 'absolute',
                      left: 0,
                      right: 0,
                      top: 0,
                      height: `${unused * 100}%`,
                      background: `repeating-linear-gradient(135deg, ${color.accent} 0 ${8 * u}px, #b73a36 ${8 * u}px ${16 * u}px)`,
                      opacity: 1 - burn * 0.85,
                      transform: `scaleY(${1 - burn * 0.25})`,
                      transformOrigin: 'bottom',
                      filter: `blur(${burn * 6}px)`,
                    }}
                  />
                </div>
                <Mono size={20 * u}>Wk {i + 1}</Mono>
              </div>
            );
          })}
        </div>
        <Words text="Every week, unused capacity expires." start={beats(2)} size={(square ? 66 : 80) * u} accent={['expires']} style={{ maxWidth: (square ? 900 : 1600) * u }} />
      </AbsoluteFill>
    </AbsoluteFill>
  );
};
