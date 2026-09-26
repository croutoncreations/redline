import React from 'react';
import { AbsoluteFill, Easing, interpolate, useCurrentFrame, useVideoConfig } from 'remotion';
import { color, font } from '../theme';

export const easeOut = Easing.bezier(0.16, 1, 0.3, 1);
export const easeInOut = Easing.bezier(0.65, 0, 0.35, 1);

/** 0→1 over [start, start+duration] with an expo-out curve. */
export const progress = (frame: number, start: number, duration: number, easing = easeOut) =>
  interpolate(frame, [start, start + duration], [0, 1], { extrapolateLeft: 'clamp', extrapolateRight: 'clamp', easing });

/** Layout unit: 1u = 1px at 1080 on the short side, for every aspect ratio. */
export const useLayout = () => {
  const { width, height } = useVideoConfig();
  const u = Math.min(width, height) / 1080;
  const square = width / height < 1.2;
  return { width, height, u, square };
};

export const Background: React.FC<{ glow?: number }> = ({ glow = 1 }) => {
  const frame = useCurrentFrame();
  const { u } = useLayout();
  const cell = 64 * u;
  return (
    <AbsoluteFill style={{ background: color.bg }}>
      <AbsoluteFill
        style={{
          background: `radial-gradient(circle at ${72 + Math.sin(frame / 80) * 4}% -12%, rgba(240,82,77,${0.24 * glow}) 0, transparent 58%)`,
        }}
      />
      <AbsoluteFill
        style={{
          backgroundImage: `linear-gradient(${color.line}40 1px, transparent 1px), linear-gradient(90deg, ${color.line}40 1px, transparent 1px)`,
          backgroundSize: `${cell}px ${cell}px`,
          backgroundPosition: `0 ${(frame * 0.4 * u) % cell}px`,
          maskImage: 'radial-gradient(ellipse at 50% 45%, black 20%, transparent 72%)',
          WebkitMaskImage: 'radial-gradient(ellipse at 50% 45%, black 20%, transparent 72%)',
          opacity: 0.45,
        }}
      />
    </AbsoluteFill>
  );
};

/** Word-by-word rise. `accent` words (exact match) render in Redline red. */
export const Words: React.FC<{
  text: string;
  start: number;
  size: number;
  stagger?: number;
  weight?: number;
  accent?: string[];
  colorOverride?: string;
  align?: 'left' | 'center';
  style?: React.CSSProperties;
}> = ({ text, start, size, stagger = 3, weight = 700, accent = [], colorOverride, align = 'center', style }) => {
  const frame = useCurrentFrame();
  return (
    <div
      style={{
        fontFamily: font.sans,
        fontSize: size,
        fontWeight: weight,
        letterSpacing: '-0.025em',
        lineHeight: 1.12,
        color: colorOverride ?? color.text,
        textAlign: align,
        textWrap: 'balance',
        ...style,
      }}
    >
      {text.split(' ').map((word, i) => {
        const p = progress(frame, start + i * stagger, 16);
        const clean = word.replace(/[.,!?]$/, '');
        return (
          <span
            key={i}
            style={{
              display: 'inline-block',
              marginRight: '0.26em',
              opacity: p,
              transform: `translateY(${(1 - p) * 0.45}em)`,
              filter: `blur(${(1 - p) * 8}px)`,
              color: accent.includes(clean) ? color.accent : undefined,
            }}
          >
            {word}
          </span>
        );
      })}
    </div>
  );
};

export const Mono: React.FC<{ children: React.ReactNode; size: number; style?: React.CSSProperties }> = ({ children, size, style }) => (
  <span style={{ fontFamily: font.mono, fontSize: size, letterSpacing: '0.08em', textTransform: 'uppercase', color: color.muted, ...style }}>{children}</span>
);

export const Eyebrow: React.FC<{ text: string; start: number }> = ({ text, start }) => {
  const frame = useCurrentFrame();
  const { u } = useLayout();
  const p = progress(frame, start, 14);
  return (
    <Mono size={22 * u} style={{ color: color.accent, fontWeight: 700, letterSpacing: '0.22em', opacity: p, display: 'block', transform: `translateY(${(1 - p) * 10}px)` }}>
      {text}
    </Mono>
  );
};

/** Fades the whole scene in and out at its edges so hard cuts land softly. */
export const SceneFade: React.FC<{ children: React.ReactNode; duration: number; fadeIn?: number; fadeOut?: number }> = ({ children, duration, fadeIn = 6, fadeOut = 6 }) => {
  const frame = useCurrentFrame();
  const opacity = Math.min(fadeIn ? progress(frame, 0, fadeIn, Easing.linear) : 1, fadeOut ? 1 - progress(frame, duration - fadeOut, fadeOut, Easing.linear) : 1);
  return <AbsoluteFill style={{ opacity }}>{children}</AbsoluteFill>;
};
