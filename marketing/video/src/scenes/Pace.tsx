import React from 'react';
import { AbsoluteFill, Img, interpolate, staticFile, useCurrentFrame } from 'remotion';
import { Background, Eyebrow, Mono, Words, easeInOut, progress, useLayout } from '../components/primitives';
import { ProviderCut, pace } from '../providers';
import { beats, color, font } from '../theme';

// The core idea: a usage bar with a "today" tick. Where the week *should* be
// versus what's actually left is the surplus, and surplus means RUN.
//
// Bar reads left→right as "used". The tick marks how much of the week has
// elapsed; if usage sits well left of it, you're behind pace with capacity to
// spare.
const days = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];

export const Pace: React.FC<{ provider: ProviderCut }> = ({ provider }) => {
  const frame = useCurrentFrame();
  const { u, square } = useLayout();
  const barW = (square ? 880 : 1360) * u;
  const barH = 64 * u;
  const used = 1 - pace.weeklyLeft / 100; // 28% used
  const elapsed = pace.daysElapsed / pace.daysInWeek; // Friday: 71% of the week gone

  const T = {
    bar: 0,
    fill: beats(1),
    tick: beats(3),
    gap: beats(5),
    verdict: beats(7.5),
    dispatch: beats(10),
  };
  const barIn = progress(frame, T.bar, 20);
  const fill = progress(frame, T.fill, beats(1.5), easeInOut) * used;
  const tick = progress(frame, T.tick, 22);
  const tickX = interpolate(tick, [0, 1], [0, elapsed]);
  const gap = progress(frame, T.gap, 24);
  const verdict = progress(frame, T.verdict, 16);
  const dispatch = progress(frame, T.dispatch, 18);
  const pulse = 0.5 + 0.5 * Math.sin((frame - T.dispatch) / 4);

  return (
    <AbsoluteFill>
      <Background glow={0.8 + gap * 0.4} />
      <AbsoluteFill style={{ alignItems: 'center', justifyContent: 'center', flexDirection: 'column', gap: (square ? 56 : 64) * u }}>
        <div style={{ textAlign: 'center', display: 'flex', flexDirection: 'column', gap: 18 * u, alignItems: 'center' }}>
          <Eyebrow text="How Redline decides" start={0} />
          <div style={{ height: (square ? 150 : 96) * u, position: 'relative', width: (square ? 900 : 1500) * u }}>
            <div style={{ position: 'absolute', inset: 0, opacity: 1 - progress(frame, T.gap - 8, 10) }}>
              <Words text="It watches your pace, not the clock." start={4} size={(square ? 60 : 72) * u} />
            </div>
            <div style={{ position: 'absolute', inset: 0 }}>
              {frame >= T.gap - 2 && <Words text="Behind pace means capacity to spare." start={T.gap} size={(square ? 60 : 72) * u} accent={['spare']} />}
            </div>
          </div>
        </div>

        <div style={{ width: barW, opacity: barIn, transform: `translateY(${(1 - barIn) * 30 * u}px)` }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 16 * u, marginBottom: 96 * u }}>
            {provider.icons.map(icon => (
              <Img key={icon.src} src={staticFile(icon.src)} style={{ width: 40 * u, height: 40 * u, filter: icon.invert ? 'invert(1)' : undefined }} />
            ))}
            <span style={{ fontFamily: font.sans, fontSize: 36 * u, fontWeight: 700, color: color.text }}>{provider.name}</span>
            <Mono size={22 * u} style={{ marginLeft: 'auto' }}>weekly allowance</Mono>
            <span style={{ fontFamily: font.mono, fontSize: 30 * u, fontWeight: 700, color: color.text }}>{Math.round((1 - fill) * 100)}% left</span>
          </div>

          <div style={{ position: 'relative', height: barH }}>
            <div style={{ position: 'absolute', inset: 0, borderRadius: barH / 2, background: color.panel2, border: `1px solid ${color.line}`, overflow: 'hidden' }}>
              <div style={{ position: 'absolute', top: 0, bottom: 0, left: 0, width: `${fill * 100}%`, background: `linear-gradient(90deg, #6b7178, ${color.muted})` }} />
              {/* surplus: between actual usage and where pace says you'd be */}
              <div
                style={{
                  position: 'absolute',
                  top: 0,
                  bottom: 0,
                  left: `${used * 100}%`,
                  width: `${(elapsed - used) * 100 * gap}%`,
                  background: `repeating-linear-gradient(135deg, rgba(240,82,77,.95) 0 ${12 * u}px, rgba(240,82,77,.7) ${12 * u}px ${24 * u}px)`,
                  boxShadow: `0 0 ${40 * u}px rgba(240,82,77,${0.6 * gap})`,
                }}
              />
            </div>
            {/* today tick */}
            <div style={{ position: 'absolute', top: -26 * u, bottom: -26 * u, left: `calc(${tickX * 100}% - ${2 * u}px)`, width: 4 * u, background: color.text, borderRadius: 2 * u, opacity: tick, boxShadow: `0 0 ${18 * u}px rgba(255,255,255,.6)` }} />
            <div style={{ position: 'absolute', top: -76 * u, left: `${tickX * 100}%`, transform: 'translateX(-50%)', opacity: tick, whiteSpace: 'nowrap' }}>
              <span style={{ fontFamily: font.mono, fontSize: 22 * u, fontWeight: 700, color: color.text, background: color.panel, border: `1px solid ${color.line}`, borderRadius: 8 * u, padding: `${6 * u}px ${12 * u}px` }}>
                TODAY · FRI
              </span>
            </div>
          </div>

          {/* day scale */}
          <div style={{ position: 'relative', height: 40 * u, marginTop: 36 * u }}>
            {days.map((d, i) => (
              <Mono key={d} size={18 * u} style={{ position: 'absolute', left: `${((i + 0.5) / 7) * 100}%`, transform: 'translateX(-50%)', opacity: barIn * (d === 'Fri' ? 1 : 0.7), color: d === 'Fri' ? color.text : color.muted }}>
                {d}
              </Mono>
            ))}
          </div>

          {/* annotations */}
          <div style={{ display: 'flex', gap: 28 * u, marginTop: 20 * u, flexWrap: 'wrap', justifyContent: square ? 'center' : 'flex-start' }}>
            <Stat label="Week elapsed" value={`${Math.round(elapsed * 100)}%`} at={T.tick + 10} />
            <Stat label="Allowance used" value={`${Math.round(used * 100)}%`} at={T.tick + 18} />
            <Stat label="Spare capacity" value={`${pace.surplus}%`} at={T.gap + 12} hot />
            <div style={{ marginLeft: square ? 0 : 'auto', display: 'flex', alignItems: 'center', gap: 16 * u, opacity: verdict, transform: `scale(${0.9 + verdict * 0.1})` }}>
              <span
                style={{
                  fontFamily: font.mono,
                  fontWeight: 800,
                  fontSize: 34 * u,
                  letterSpacing: '0.12em',
                  color: '#fff',
                  background: color.accent,
                  borderRadius: 12 * u,
                  padding: `${12 * u}px ${24 * u}px`,
                  boxShadow: `0 0 ${(30 + pulse * 30 * dispatch) * u}px rgba(240,82,77,.7)`,
                }}
              >
                RUN
              </span>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 4 * u }}>
                <span style={{ fontFamily: font.sans, fontSize: 26 * u, fontWeight: 650, color: color.text }}>Run now · high surplus</span>
                <Mono size={17 * u} style={{ opacity: dispatch, letterSpacing: '0.04em', textTransform: 'none' }}>
                  → dispatching “{pace.job}”
                </Mono>
              </div>
            </div>
          </div>
        </div>
      </AbsoluteFill>
    </AbsoluteFill>
  );
};

const Stat: React.FC<{ label: string; value: string; at: number; hot?: boolean }> = ({ label, value, at, hot }) => {
  const frame = useCurrentFrame();
  const { u } = useLayout();
  const p = progress(frame, at, 16);
  return (
    <div style={{ opacity: p, transform: `translateY(${(1 - p) * 16 * u}px)`, padding: `${14 * u}px ${20 * u}px`, borderRadius: 14 * u, background: hot ? color.accentSoft : color.panel, border: `1px solid ${hot ? 'rgba(240,82,77,.5)' : color.line}` }}>
      <Mono size={15 * u} style={{ display: 'block', color: hot ? '#ff8b86' : color.muted }}>{label}</Mono>
      <span style={{ fontFamily: font.mono, fontSize: 38 * u, fontWeight: 700, color: hot ? color.accent : color.text }}>{value}</span>
    </div>
  );
};
