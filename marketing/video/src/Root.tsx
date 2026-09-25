import React from 'react';
import { Composition } from 'remotion';
import { Loop, loopDuration } from './Loop';
import { Promo, promoDuration } from './Promo';
import { ProviderId } from './providers';
import { FPS } from './theme';

const formats = [
  { suffix: 'Wide', width: 1920, height: 1080 },
  { suffix: 'Square', width: 1080, height: 1080 },
];
const cuts: { id: ProviderId; label: string }[] = [
  { id: 'claude', label: 'Claude' },
  { id: 'codex', label: 'Codex' },
];

export const Root: React.FC = () => (
  <>
    {cuts.flatMap(cut =>
      formats.flatMap(f => [
        <Composition key={`p-${cut.id}-${f.suffix}`} id={`Promo${cut.label}${f.suffix}`} component={Promo} durationInFrames={promoDuration()} fps={FPS} width={f.width} height={f.height} defaultProps={{ provider: cut.id }} />,
        <Composition key={`l-${cut.id}-${f.suffix}`} id={`Loop${cut.label}${f.suffix}`} component={Loop} durationInFrames={loopDuration()} fps={FPS} width={f.width} height={f.height} defaultProps={{ provider: cut.id }} />,
      ]),
    )}
  </>
);
