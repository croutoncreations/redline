import React from 'react';
import { AbsoluteFill, Img, staticFile, useCurrentFrame } from 'remotion';
import { Background, Mono, Words, progress, useLayout } from '../components/primitives';
import { ProviderCut } from '../providers';
import { beats, color, font } from '../theme';

// Trust layer plus the "works with the others" mention.
const points = [
  { title: 'A reserve that stays yours', body: 'Background work only spends above it' },
  { title: 'Off until you turn it on', body: 'Stale data means wait, never run' },
  { title: 'Local only', body: 'No cloud, no account, no telemetry' },
];

export const Features: React.FC<{ provider: ProviderCut }> = ({ provider }) => {
  const frame = useCurrentFrame();
  const { u, square } = useLayout();
  const others = progress(frame, beats(5), 18);
  return (
    <AbsoluteFill>
      <Background />
      <AbsoluteFill style={{ alignItems: 'center', justifyContent: 'center', flexDirection: 'column', gap: (square ? 44 : 60) * u, padding: `0 ${90 * u}px` }}>
        <Words text="Safe by default." start={0} size={(square ? 70 : 80) * u} />
        <div style={{ display: 'flex', flexDirection: square ? 'column' : 'row', gap: 26 * u }}>
          {points.map((point, i) => {
            const p = progress(frame, beats(1) + i * beats(0.75), 18);
            return (
              <div
                key={point.title}
                style={{
                  width: (square ? 820 : 500) * u,
                  padding: `${(square ? 26 : 34) * u}px ${34 * u}px`,
                  borderRadius: 20 * u,
                  background: color.panel,
                  border: `1px solid ${color.line}`,
                  borderTop: `${3 * u}px solid ${color.accent}`,
                  opacity: p,
                  transform: `translateY(${(1 - p) * 40 * u}px)`,
                }}
              >
                <div style={{ fontFamily: font.sans, fontSize: 36 * u, fontWeight: 700, color: color.text }}>{point.title}</div>
                <div style={{ fontFamily: font.sans, fontSize: 26 * u, color: color.muted, marginTop: 10 * u }}>{point.body}</div>
              </div>
            );
          })}
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 20 * u, opacity: others, transform: `translateY(${(1 - others) * 20 * u}px)` }}>
          <Img src={staticFile('brand/claude.svg')} style={{ width: 44 * u, height: 44 * u }} />
          <Img src={staticFile('brand/codex.svg')} style={{ width: 44 * u, height: 44 * u, filter: 'invert(1)' }} />
          <Mono size={26 * u} style={{ fontSize: 26 * u, letterSpacing: '0.06em', color: color.text, textTransform: 'none', fontFamily: font.sans, fontWeight: 600 }}>
            Built for {provider.name}. {provider.others}.
          </Mono>
        </div>
      </AbsoluteFill>
    </AbsoluteFill>
  );
};
