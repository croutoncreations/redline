import React from 'react';
import { AbsoluteFill, Sequence, useCurrentFrame } from 'remotion';
import { Gauge } from './components/Gauge';
import { SceneFade, progress, useLayout } from './components/primitives';
import { PromoProps } from './Promo';
import { providers } from './providers';
import { Pace } from './scenes/Pace';
import { bars, color, font } from './theme';

// Silent social loop: the pace explainer ending on RUN, then a brand card.
// Starts and ends on the same dark frame so the GIF loops without a jump.
export const LOOP_PACE = bars(3.5);
export const LOOP_CARD = bars(1);
export const loopDuration = () => LOOP_PACE + LOOP_CARD;

const Card: React.FC = () => {
  const frame = useCurrentFrame();
  const { u } = useLayout();
  const p = progress(frame, 0, 14);
  return (
    <AbsoluteFill style={{ background: color.bg, alignItems: 'center', justifyContent: 'center', flexDirection: 'column', gap: 22 * u, opacity: p }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 22 * u }}>
        <Gauge size={120 * u} />
        <span style={{ fontFamily: font.sans, fontWeight: 800, fontSize: 84 * u, letterSpacing: '0.12em', color: color.text }}>REDLINE</span>
      </div>
      <span style={{ fontFamily: font.mono, fontSize: 30 * u, color: color.muted }}>github.com/croutoncreations/redline</span>
    </AbsoluteFill>
  );
};

export const Loop: React.FC<PromoProps> = ({ provider }) => (
  <AbsoluteFill style={{ background: color.bg }}>
    <Sequence durationInFrames={LOOP_PACE}>
      <SceneFade duration={LOOP_PACE} fadeIn={0} fadeOut={8}>
        <Pace provider={providers[provider]} />
      </SceneFade>
    </Sequence>
    <Sequence from={LOOP_PACE} durationInFrames={LOOP_CARD}>
      <SceneFade duration={LOOP_CARD} fadeIn={0} fadeOut={10}>
        <Card />
      </SceneFade>
    </Sequence>
  </AbsoluteFill>
);
