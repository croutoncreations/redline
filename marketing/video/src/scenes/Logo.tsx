import React from 'react';
import { AbsoluteFill, Easing, interpolate, useCurrentFrame } from 'remotion';
import { Gauge } from '../components/Gauge';
import { Background, Words, easeOut, progress, useLayout } from '../components/primitives';
import { beats, color, font } from '../theme';

export const Logo: React.FC = () => {
  const frame = useCurrentFrame();
  const { u, square } = useLayout();
  const draw = progress(frame, 0, beats(1.5), Easing.bezier(0.5, 0, 0.2, 1));
  const redline = progress(frame, beats(1.25), 14);
  // Needle overshoots toward the redline, then settles just short of it.
  const needle = interpolate(frame, [beats(1.5), beats(2.25), beats(2.75)], [0, 1.08, 1], { extrapolateLeft: 'clamp', extrapolateRight: 'clamp', easing: easeOut });
  const word = progress(frame, beats(2), 22);
  const size = (square ? 360 : 380) * u;
  return (
    <AbsoluteFill>
      <Background glow={0.6 + redline * 0.6} />
      <AbsoluteFill style={{ alignItems: 'center', justifyContent: 'center', flexDirection: 'column', gap: 18 * u }}>
        <div style={{ transform: `scale(${0.9 + draw * 0.1})` }}>
          <Gauge size={size} draw={draw} redline={redline} notches={draw} needle={Math.max(0, needle)} tile={progress(frame, 0, 18)} />
        </div>
        <div
          style={{
            fontFamily: font.sans,
            fontWeight: 800,
            fontSize: 132 * u,
            letterSpacing: `${0.12 + (1 - word) * 0.2}em`,
            color: color.text,
            opacity: word,
            marginRight: '-0.12em',
          }}
        >
          REDLINE
        </div>
        <Words text="Turn spare subscription capacity into finished work." start={beats(3)} size={(square ? 40 : 46) * u} weight={500} colorOverride={color.muted} stagger={2} style={{ maxWidth: (square ? 860 : 1400) * u }} />
      </AbsoluteFill>
    </AbsoluteFill>
  );
};
