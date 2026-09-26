import React from 'react';
import { AbsoluteFill, Img, Sequence, staticFile, useCurrentFrame } from 'remotion';
import { Background, Eyebrow, Mono, Words, progress, useLayout } from '../components/primitives';
import { ProviderCut } from '../providers';
import { beats, color, font } from '../theme';

// "Queue it from wherever you are": terminal → your agent (MCP) → menu bar.
// Every string mirrors shipped behavior: `redline later` output is verbatim
// from internal/cli/later.go; the agent turn uses the real MCP tool name.
export const ANYWHERE_BEATS = { terminal: 6, agent: 7, menubar: 4 };
export const anywhereDuration = () => beats(ANYWHERE_BEATS.terminal + ANYWHERE_BEATS.agent + ANYWHERE_BEATS.menubar);

const Frame: React.FC<{ eyebrow: string; title: string; children: React.ReactNode; accent?: string[] }> = ({ eyebrow, title, children, accent }) => {
  const { u, square } = useLayout();
  return (
    <AbsoluteFill>
      <Background />
      <AbsoluteFill
        style={{
          flexDirection: square ? 'column' : 'row',
          alignItems: 'center',
          justifyContent: 'center',
          gap: (square ? 44 : 80) * u,
          padding: `0 ${(square ? 60 : 90) * u}px`,
        }}
      >
        <div style={{ width: (square ? 900 : 560) * u, flexShrink: 0, display: 'flex', flexDirection: 'column', gap: 22 * u, alignItems: square ? 'center' : 'flex-start' }}>
          <Eyebrow text={eyebrow} start={0} />
          <Words text={title} start={4} size={(square ? 58 : 72) * u} align={square ? 'center' : 'left'} accent={accent} />
        </div>
        {children}
      </AbsoluteFill>
    </AbsoluteFill>
  );
};

const Window: React.FC<{ title: string; width: number; children: React.ReactNode }> = ({ title, width, children }) => {
  const frame = useCurrentFrame();
  const { u } = useLayout();
  const p = progress(frame, 2, 20);
  return (
    <div
      style={{
        width,
        borderRadius: 18 * u,
        background: '#0b0d10',
        border: `1px solid ${color.line}`,
        boxShadow: '0 40px 90px rgba(0,0,0,.55)',
        overflow: 'hidden',
        opacity: p,
        transform: `translateY(${(1 - p) * 50 * u}px) scale(${0.96 + p * 0.04})`,
      }}
    >
      <div style={{ height: 50 * u, display: 'flex', alignItems: 'center', gap: 10 * u, padding: `0 ${20 * u}px`, background: color.panel, borderBottom: `1px solid ${color.line}` }}>
        {['#ff5f57', '#febc2e', '#28c840'].map(c => (
          <i key={c} style={{ width: 14 * u, height: 14 * u, borderRadius: 99, background: c, opacity: 0.85 }} />
        ))}
        <Mono size={17 * u} style={{ marginLeft: 12 * u, textTransform: 'none', letterSpacing: '0.02em' }}>{title}</Mono>
      </div>
      <div style={{ padding: `${28 * u}px ${32 * u}px ${34 * u}px` }}>{children}</div>
    </div>
  );
};

const typed = (text: string, frame: number, start: number, cps = 1.4) => text.slice(0, Math.max(0, Math.floor((frame - start) * cps)));

const Terminal: React.FC<{ provider: ProviderCut }> = ({ provider }) => {
  const frame = useCurrentFrame();
  const { u, square } = useLayout();
  // `later` matches a profile for the current repo; its --harness default is
  // claude-code, so the Codex cut names its harness explicitly.
  const cmd = provider.id === 'codex' ? 'redline later --harness codex-cli "fix the flaky auth test"' : 'redline later "fix the flaky auth test"';
  const profile = provider.id === 'codex' ? 'atlas-codex' : 'atlas-claude';
  const typeStart = 16;
  const done = typeStart + cmd.length / 1.4;
  const out = progress(frame, done + 10, 10);
  const cursor = Math.floor(frame / 15) % 2 === 0;
  const size = (square ? 25 : 31) * u;
  return (
    <Frame eyebrow="From your terminal" title="Queue it the moment you think of it.">
      <Window title="~/projects/atlas — zsh" width={(square ? 900 : 1100) * u}>
        <div style={{ fontFamily: font.mono, fontSize: size, lineHeight: 1.6, color: color.text, whiteSpace: 'pre-wrap' }}>
          <span style={{ color: color.green }}>atlas</span> <span style={{ color: color.muted }}>main</span> <span style={{ color: color.accent }}>❯</span> {typed(cmd, frame, typeStart)}
          {frame < done + 10 && <span style={{ opacity: cursor ? 1 : 0, background: color.text }}>&nbsp;</span>}
          <div style={{ opacity: out, marginTop: 10 * u }}>
            <div>
              Queued Redline task <span style={{ color: color.amber }}>d8a375f6</span>… <span style={{ color: color.muted }}>(tier behind, profile {profile}).</span>
            </div>
            <div style={{ color: color.muted, marginTop: 6 * u }}>
              It runs only when usage is behind pace and above your reserve. <span style={{ color: color.text }}>It is never forced.</span>
            </div>
          </div>
        </div>
      </Window>
    </Frame>
  );
};

const Bubble: React.FC<{ at: number; side: 'user' | 'agent'; children: React.ReactNode }> = ({ at, side, children }) => {
  const frame = useCurrentFrame();
  const { u, square } = useLayout();
  const p = progress(frame, at, 16);
  return (
    <div style={{ display: 'flex', justifyContent: side === 'user' ? 'flex-end' : 'flex-start', opacity: p, transform: `translateY(${(1 - p) * 24 * u}px)` }}>
      <div
        style={{
          maxWidth: '86%',
          padding: `${18 * u}px ${24 * u}px`,
          borderRadius: 18 * u,
          fontFamily: font.sans,
          fontSize: (square ? 27 : 31) * u,
          lineHeight: 1.4,
          color: color.text,
          background: side === 'user' ? '#2a2f36' : color.panel,
          border: `1px solid ${side === 'user' ? '#3a4048' : color.line}`,
        }}
      >
        {children}
      </div>
    </div>
  );
};

const Agent: React.FC<{ provider: ProviderCut }> = ({ provider }) => {
  const frame = useCurrentFrame();
  const { u, square } = useLayout();
  const tool = progress(frame, beats(2), 12);
  return (
    <Frame eyebrow="From your agent" title="Or just ask your agent.">
      <Window title={`${provider.name} · redline MCP`} width={(square ? 900 : 1100) * u}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 18 * u }}>
          <Bubble at={beats(0.75)} side="user">Queue a sweep of the stale TODOs for later. How much spare capacity do I have this week?</Bubble>
          <div style={{ opacity: tool, display: 'flex', gap: 12 * u, alignItems: 'center', fontFamily: font.mono, fontSize: (square ? 20 : 22) * u, color: color.muted }}>
            <Img src={staticFile('brand/redline-icon.svg')} style={{ width: 30 * u, height: 30 * u }} />
            <span>redline_task_create</span>
            <span style={{ color: color.line }}>·</span>
            <span>redline_provider_capacity</span>
            <span style={{ color: color.green, marginLeft: 'auto' }}>✓</span>
          </div>
          <Bubble at={beats(3)} side="agent">
            Queued <b>“Sweep stale TODOs”</b>. You're <span style={{ color: color.accent }}>43% behind pace</span> with two days until reset, so it's eligible now.
            Redline will pick it up on its next check.
          </Bubble>
        </div>
      </Window>
    </Frame>
  );
};

const MenuBar: React.FC = () => {
  const frame = useCurrentFrame();
  const { u, square } = useLayout();
  const p = progress(frame, 4, 22);
  const h = (square ? 700 : 860) * u;
  const w = (h * 840) / 1278; // native capture aspect
  return (
    <Frame eyebrow="From your menu bar" title="Glance at it anytime.">
      <div style={{ position: 'relative', opacity: p, transform: `translateY(${(1 - p) * -60 * u}px)` }}>
        <div
          style={{
            position: 'absolute',
            top: -26 * u,
            left: '50%',
            width: 28 * u,
            height: 28 * u,
            background: '#2d3136',
            transform: 'translateX(-50%) rotate(45deg)',
            borderRadius: 4 * u,
          }}
        />
        <Img
          src={staticFile('captures/native-quick-panel.png')}
          style={{ position: 'relative', height: h, width: w, borderRadius: 20 * u, border: `1px solid #40464e`, boxShadow: '0 50px 110px rgba(0,0,0,.6)' }}
        />
      </div>
    </Frame>
  );
};

export const Anywhere: React.FC<{ provider: ProviderCut }> = ({ provider }) => {
  const b = ANYWHERE_BEATS;
  return (
    <AbsoluteFill>
      <Sequence durationInFrames={beats(b.terminal)}>
        <Terminal provider={provider} />
      </Sequence>
      <Sequence from={beats(b.terminal)} durationInFrames={beats(b.agent)}>
        <Agent provider={provider} />
      </Sequence>
      <Sequence from={beats(b.terminal + b.agent)} durationInFrames={beats(b.menubar)}>
        <MenuBar />
      </Sequence>
    </AbsoluteFill>
  );
};
