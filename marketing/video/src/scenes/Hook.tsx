import React from 'react';
import { AbsoluteFill, interpolate, useCurrentFrame } from 'remotion';
import { Background, Mono, Words, progress, useLayout } from '../components/primitives';
import { beats, color, font } from '../theme';

// A weekly allowance counts down to its reset; the unused part expires.
export const Hook: React.FC = () => {
  const frame = useCurrentFrame();
  const { u, square } = useLayout();
  const resetAt = beats(4.5);
  const unused = 0.38;
  const expired = progress(frame, resetAt, 16);
  const secondsLeft = Math.max(0, Math.ceil((resetAt - frame) / 30));
  const flash = interpolate(frame, [resetAt, resetAt + 4, resetAt + 16], [0, 1, 0], { extrapolateLeft: 'clamp', extrapolateRight: 'clamp' });
  const barW = (square ? 860 : 1180) * u;
  const enter = progress(frame, 0, 20);

  return (
    <AbsoluteFill>
      <Background glow={0.6 + flash} />
      <AbsoluteFill style={{ alignItems: 'center', justifyContent: 'center', gap: 64 * u, flexDirection: 'column' }}>
        <div style={{ width: barW, opacity: enter, transform: `translateY(${(1 - enter) * 30 * u}px)` }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: 20 * u }}>
            <Mono size={24 * u} style={{ letterSpacing: '0.18em' }}>Weekly allowance</Mono>
            <span style={{ fontFamily: font.mono, fontSize: 30 * u, color: expired > 0 ? color.accent : color.text, fontWeight: 700 }}>
              {expired > 0 ? 'RESET' : `Resets in 00:00:0${secondsLeft}`}
            </span>
          </div>
          <div style={{ position: 'relative', height: 44 * u, borderRadius: 22 * u, background: color.panel2, border: `1px solid ${color.line}`, overflow: 'hidden' }}>
            <div style={{ position: 'absolute', inset: 0, width: `${(1 - unused) * 100}%`, background: color.muted, opacity: 0.55 }} />
            <div
              style={{
                position: 'absolute',
                top: 0,
                bottom: 0,
                left: `${(1 - unused) * 100}%`,
                width: `${unused * 100 * (1 - expired)}%`,
                background: `repeating-linear-gradient(135deg, ${color.accent} 0 ${10 * u}px, #c7403c ${10 * u}px ${20 * u}px)`,
                boxShadow: `0 0 ${30 * u}px ${color.accent}`,
                opacity: 1 - expired * 0.6,
              }}
            />
          </div>
          <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 18 * u }}>
            <Mono size={20 * u}>Used</Mono>
            <Mono size={20 * u} style={{ color: expired > 0 ? color.muted : color.accent }}>
              {expired > 0 ? 'Unused · expired' : `${Math.round(unused * 100)}% unused`}
            </Mono>
          </div>
        </div>
        <div style={{ height: (square ? 260 : 170) * u, width: (square ? 900 : 1500) * u, position: 'relative' }}>
          <div style={{ position: 'absolute', inset: 0, opacity: 1 - progress(frame, resetAt - 6, 8), filter: `blur(${progress(frame, resetAt - 6, 8) * 10}px)` }}>
            <Words text="Your subscription resets every week." start={8} size={(square ? 72 : 84) * u} />
          </div>
          <div style={{ position: 'absolute', inset: 0 }}>
            {expired > 0 && <Words text="Whatever you didn't use is gone." start={resetAt + 3} size={(square ? 72 : 84) * u} accent={['gone']} />}
          </div>
        </div>
      </AbsoluteFill>
    </AbsoluteFill>
  );
};
