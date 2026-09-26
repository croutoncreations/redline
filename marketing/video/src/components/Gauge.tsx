import React from 'react';
import { color } from '../theme';

// Animated version of macos/Sources/RedlineMenuBar/Resources/AppIcon.svg.
// draw/redline/notches/needle are 0→1 progress values driven by the scene.
export const Gauge: React.FC<{
  size: number;
  draw?: number;
  redline?: number;
  notches?: number;
  needle?: number; // 0 = parked at the left stop, 1 = resting just short of the redline
  tile?: number; // opacity of the rounded-square tile behind the dial
}> = ({ size, draw = 1, redline = 1, notches = 1, needle = 1, tile = 1 }) => {
  const needleRotation = -100 * (1 - needle); // 155° stop → 55° rest, clockwise in SVG space
  return (
    <svg width={size} height={size} viewBox="0 0 1024 1024" style={{ overflow: 'visible' }}>
      <defs>
        <linearGradient id="gauge-bg" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#29292d" />
          <stop offset="1" stopColor="#111114" />
        </linearGradient>
        <filter id="gauge-glow" x="-50%" y="-50%" width="200%" height="200%">
          <feGaussianBlur stdDeviation="18" result="b" />
          <feMerge>
            <feMergeNode in="b" />
            <feMergeNode in="SourceGraphic" />
          </feMerge>
        </filter>
      </defs>
      <rect x="72" y="72" width="880" height="880" rx="204" fill="url(#gauge-bg)" opacity={tile} />
      <path
        d="M231 519 A310 310 0 0 1 793 519"
        fill="none"
        stroke={color.logoWhite}
        strokeWidth="64"
        strokeLinecap="round"
        pathLength={1}
        strokeDasharray="1 1"
        strokeDashoffset={1 - draw}
        opacity={draw > 0.001 ? 0.95 : 0}
      />
      <path
        d="M719.4 419.6 A310 310 0 0 1 793 519"
        fill="none"
        stroke={color.logoRed}
        strokeWidth="64"
        strokeLinecap="round"
        pathLength={1}
        strokeDasharray="1 1"
        strokeDashoffset={1 - redline}
        opacity={redline > 0.001 ? 1 : 0}
        filter="url(#gauge-glow)"
      />
      <g stroke="#111114" strokeWidth="18" strokeLinecap="round" opacity={0.75 * notches}>
        <path d="M276.9 485.4 L239.2 459" />
        <path d="M390.7 389.9 L371.3 348.2" />
        <path d="M633.3 389.9 L652.7 348.2" />
        <path d="M747.1 485.4 L784.8 459" />
      </g>
      <g transform={`rotate(${needleRotation} 512 650)`} opacity={needle > 0 || draw >= 1 ? 1 : 0}>
        <path d="M487.4 632.8 L656.5 443.6 L536.6 667.2 Z" fill={color.logoWhite} />
      </g>
      <circle cx="512" cy="650" r="62" fill={color.logoWhite} opacity={draw > 0.6 ? 1 : 0} />
      <circle cx="512" cy="650" r="26" fill={color.logoRed} opacity={draw > 0.6 ? 1 : 0} />
    </svg>
  );
};
