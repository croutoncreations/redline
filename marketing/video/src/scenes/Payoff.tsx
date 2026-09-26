import React from 'react';
import { AbsoluteFill, interpolate, useCurrentFrame } from 'remotion';
import { Background, Mono, Words, easeInOut, progress, useLayout } from '../components/primitives';
import { beats, color, font } from '../theme';

// Callback to the hook: the same weekly bar, but this time the unused slice
// turns into finished jobs before the reset instead of expiring.
const done = ['Fixed a flaky auth test', 'Swept 14 stale TODOs', 'Drafted release notes', 'Bumped 3 dependencies'];

export const Payoff: React.FC = () => {
  const frame = useCurrentFrame();
  const { u, square } = useLayout();
  const barW = (square ? 860 : 1180) * u;
  const used = 0.62;
  const reserve = 0.1;
  const fill = interpolate(progress(frame, beats(1), beats(4), easeInOut), [0, 1], [used, 1 - reserve]);
  const enter = progress(frame, 0, 18);
  return (
    <AbsoluteFill>
      <Background glow={1.1} />
      <AbsoluteFill style={{ alignItems: 'center', justifyContent: 'center', flexDirection: 'column', gap: (square ? 44 : 54) * u }}>
        <div style={{ width: barW, opacity: enter }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 18 * u }}>
            <Mono size={22 * u} style={{ letterSpacing: '0.18em' }}>Weekly allowance</Mono>
            <span style={{ fontFamily: font.mono, fontSize: 28 * u, fontWeight: 700, color: color.text }}>Resets in 6 hrs</span>
          </div>
          <div style={{ position: 'relative', height: 44 * u, borderRadius: 22 * u, background: color.panel2, border: `1px solid ${color.line}`, overflow: 'hidden' }}>
            <div style={{ position: 'absolute', inset: 0, width: `${used * 100}%`, background: color.muted, opacity: 0.55 }} />
            <div style={{ position: 'absolute', top: 0, bottom: 0, left: `${used * 100}%`, width: `${(fill - used) * 100}%`, background: `linear-gradient(90deg, #3fa877, ${color.green})`, boxShadow: `0 0 ${30 * u}px rgba(114,217,164,.55)` }} />
            <div style={{ position: 'absolute', top: 0, bottom: 0, right: 0, width: `${reserve * 100}%`, background: `repeating-linear-gradient(135deg, #2a3036 0 ${8 * u}px, #20252a ${8 * u}px ${16 * u}px)`, borderLeft: `${2 * u}px solid ${color.muted}` }} />
          </div>
          <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 16 * u }}>
            <Mono size={19 * u}>Your work</Mono>
            <Mono size={19 * u} style={{ color: color.green }}>Spare capacity → finished jobs</Mono>
            <Mono size={19 * u}>Reserve</Mono>
          </div>
        </div>
        <div style={{ display: 'grid', gridTemplateColumns: square ? '1fr' : '1fr 1fr', gap: 16 * u, width: barW }}>
          {done.map((job, i) => {
            const p = progress(frame, beats(1.75) + i * beats(0.75), 14);
            return (
              <div key={job} style={{ display: 'flex', alignItems: 'center', gap: 16 * u, padding: `${16 * u}px ${22 * u}px`, borderRadius: 14 * u, background: color.panel, border: `1px solid ${color.line}`, opacity: p, transform: `translateY(${(1 - p) * 20 * u}px)` }}>
                <span style={{ width: 32 * u, height: 32 * u, borderRadius: 99, background: color.green, color: '#0d0f12', display: 'grid', placeItems: 'center', fontWeight: 800, fontSize: 20 * u }}>✓</span>
                <span style={{ fontFamily: font.sans, fontSize: 28 * u, fontWeight: 600, color: color.text }}>{job}</span>
              </div>
            );
          })}
        </div>
        <Words text="This time, nothing expires." start={beats(5)} size={(square ? 62 : 74) * u} accent={['nothing']} />
      </AbsoluteFill>
    </AbsoluteFill>
  );
};
