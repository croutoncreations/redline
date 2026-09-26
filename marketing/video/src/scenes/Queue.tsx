import React from 'react';
import { AbsoluteFill, useCurrentFrame } from 'remotion';
import { Background, Eyebrow, Mono, Words, progress, useLayout } from '../components/primitives';
import { beats, color, font } from '../theme';

// Same job names as the demo fixtures, so the queue matches the UI later.
const jobs = [
  { name: 'Find and fix one real bug', tier: 'Standard surplus', priority: 'P90' },
  { name: 'Improve test coverage', tier: 'High surplus', priority: 'P70' },
  { name: 'Draft release notes', tier: 'Standard surplus', priority: 'P60' },
  { name: 'Review dependency health', tier: 'Near expiry', priority: 'P40' },
];

export const Queue: React.FC = () => {
  const frame = useCurrentFrame();
  const { u, square } = useLayout();
  const cardW = (square ? 820 : 900) * u;
  return (
    <AbsoluteFill>
      <Background />
      <AbsoluteFill
        style={{
          alignItems: 'center',
          justifyContent: 'center',
          flexDirection: square ? 'column' : 'row',
          gap: (square ? 56 : 110) * u,
          padding: `0 ${120 * u}px`,
        }}
      >
        <div style={{ width: (square ? 900 : 700) * u, display: 'flex', flexDirection: 'column', gap: 26 * u, alignItems: square ? 'center' : 'flex-start' }}>
          <Eyebrow text="Dispatch queue" start={0} />
          <Words text="Queue the work you never get to." start={4} size={(square ? 64 : 76) * u} align={square ? 'center' : 'left'} />
          <Words
            text="Redline runs it only when you have extra capacity."
            start={beats(4)}
            size={(square ? 38 : 42) * u}
            weight={500}
            colorOverride={color.muted}
            accent={['extra', 'capacity']}
            stagger={2}
            align={square ? 'center' : 'left'}
          />
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 18 * u }}>
          {jobs.map((job, i) => {
            const p = progress(frame, beats(1) + i * beats(0.5), 18);
            return (
              <div
                key={job.name}
                style={{
                  width: cardW,
                  display: 'flex',
                  alignItems: 'center',
                  gap: 28 * u,
                  padding: `${(square ? 22 : 26) * u}px ${30 * u}px`,
                  borderRadius: 18 * u,
                  background: color.panel,
                  border: `1px solid ${color.line}`,
                  boxShadow: '0 20px 50px rgba(0,0,0,.35)',
                  opacity: p,
                  transform: `translateX(${(1 - p) * 120 * u}px)`,
                }}
              >
                <span style={{ fontFamily: font.mono, fontWeight: 700, fontSize: 26 * u, color: color.accent, width: 70 * u }}>{job.priority}</span>
                <span style={{ fontFamily: font.sans, fontWeight: 650, fontSize: 34 * u, color: color.text, flex: 1 }}>{job.name}</span>
                <Mono size={17 * u} style={{ border: `1px solid ${color.line}`, borderRadius: 8 * u, padding: `${8 * u}px ${12 * u}px` }}>{job.tier}</Mono>
                <span style={{ display: 'flex', alignItems: 'center', gap: 10 * u }}>
                  <i style={{ width: 12 * u, height: 12 * u, borderRadius: 99, background: color.green }} />
                  <Mono size={18 * u} style={{ color: color.text }}>Queued</Mono>
                </span>
              </div>
            );
          })}
        </div>
      </AbsoluteFill>
    </AbsoluteFill>
  );
};
