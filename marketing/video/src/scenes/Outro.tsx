import React from 'react';
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import { Gauge } from '../components/Gauge';
import { Background, Mono, Words, progress, useLayout } from '../components/primitives';
import { ProviderCut } from '../providers';
import { beats, color, font } from '../theme';

export const Outro: React.FC<{ provider: ProviderCut }> = ({ provider }) => {
  const frame = useCurrentFrame();
  const { u, square } = useLayout();
  const logo = progress(frame, 0, 20);
  const cmd = progress(frame, beats(2.5), 18);
  const url = progress(frame, beats(3.5), 18);
  return (
    <AbsoluteFill>
      <Background glow={1.3} />
      <AbsoluteFill style={{ alignItems: 'center', justifyContent: 'center', flexDirection: 'column', gap: (square ? 40 : 44) * u }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 26 * u, opacity: logo, transform: `scale(${0.92 + logo * 0.08})` }}>
          <Gauge size={150 * u} />
          <span style={{ fontFamily: font.sans, fontWeight: 800, fontSize: 96 * u, letterSpacing: '0.12em', color: color.text }}>REDLINE</span>
        </div>
        <Words text={provider.headline} start={beats(1)} size={(square ? 60 : 70) * u} accent={['waste']} style={{ maxWidth: (square ? 920 : 1600) * u }} />
        <div
          style={{
            fontFamily: font.mono,
            fontSize: (square ? 30 : 34) * u,
            color: color.text,
            background: color.panel,
            border: `1px solid ${color.line}`,
            borderRadius: 14 * u,
            padding: `${20 * u}px ${34 * u}px`,
            opacity: cmd,
            transform: `translateY(${(1 - cmd) * 20 * u}px)`,
          }}
        >
          <span style={{ color: color.accent }}>$ </span>brew install --cask croutoncreations/tap/redline
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 14 * u, opacity: url }}>
          <span style={{ fontFamily: font.sans, fontSize: 36 * u, fontWeight: 650, color: color.text }}>github.com/croutoncreations/redline</span>
          <Mono size={22 * u} style={{ letterSpacing: '0.2em' }}>Free · Open source · macOS</Mono>
        </div>
      </AbsoluteFill>
    </AbsoluteFill>
  );
};
