import React from 'react';
import { AbsoluteFill, Img, staticFile, useCurrentFrame } from 'remotion';
import { Gauge } from '../components/Gauge';
import { Background, Mono, Words, progress, useLayout } from '../components/primitives';
import { ProviderCut } from '../providers';
import { beats, color, font } from '../theme';

// The pitch lands last. Trust points ride underneath as one quiet line
// instead of a scene of their own.
export const Outro: React.FC<{ provider: ProviderCut }> = ({ provider }) => {
  const frame = useCurrentFrame();
  const { u, square } = useLayout();
  const logo = progress(frame, beats(2.5), 18);
  const cmd = progress(frame, beats(3.5), 18);
  const trust = progress(frame, beats(4.5), 18);
  const others = progress(frame, beats(5.25), 18);
  const headSize = (square ? 84 : 112) * u;
  return (
    <AbsoluteFill>
      <Background glow={1.4} />
      <AbsoluteFill style={{ alignItems: 'center', justifyContent: 'center', flexDirection: 'column', gap: (square ? 34 : 38) * u, padding: `0 ${60 * u}px` }}>
        <Words text={provider.headline} start={0} size={headSize} weight={800} stagger={4} accent={['waste']} style={{ maxWidth: (square ? 960 : 1700) * u, letterSpacing: '-0.035em' }} />
        <div style={{ display: 'flex', alignItems: 'center', gap: 22 * u, opacity: logo, transform: `translateY(${(1 - logo) * 16 * u}px)`, marginTop: 10 * u }}>
          <Gauge size={96 * u} />
          <span style={{ fontFamily: font.sans, fontWeight: 800, fontSize: 64 * u, letterSpacing: '0.12em', color: color.text }}>REDLINE</span>
        </div>
        <div
          style={{
            fontFamily: font.mono,
            fontSize: (square ? 27 : 32) * u,
            color: color.text,
            background: color.panel,
            border: `1px solid ${color.line}`,
            borderRadius: 14 * u,
            padding: `${18 * u}px ${30 * u}px`,
            opacity: cmd,
            transform: `translateY(${(1 - cmd) * 16 * u}px)`,
          }}
        >
          <span style={{ color: color.accent }}>$ </span>brew install --cask croutoncreations/tap/redline
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 12 * u, opacity: trust }}>
          <span style={{ fontFamily: font.sans, fontSize: 34 * u, fontWeight: 650, color: color.text }}>github.com/croutoncreations/redline</span>
          <Mono size={(square ? 17 : 20) * u} style={{ letterSpacing: '0.16em', textAlign: 'center' }}>
            Free & open source · Local only · Your reserve stays yours
          </Mono>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 14 * u, opacity: others * 0.9 }}>
          <Img src={staticFile('brand/claude.svg')} style={{ width: 30 * u, height: 30 * u }} />
          <Img src={staticFile('brand/codex.svg')} style={{ width: 30 * u, height: 30 * u, filter: 'invert(1)' }} />
          <span style={{ fontFamily: font.sans, fontSize: 24 * u, color: color.muted }}>{provider.others}</span>
        </div>
      </AbsoluteFill>
    </AbsoluteFill>
  );
};
