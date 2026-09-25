import React from 'react';
import { AbsoluteFill, Audio, Sequence, interpolate, staticFile, useVideoConfig } from 'remotion';
import { SceneFade } from './components/primitives';
import { ProviderId, providers } from './providers';
import { Features } from './scenes/Features';
import { Hook } from './scenes/Hook';
import { Logo } from './scenes/Logo';
import { Outro } from './scenes/Outro';
import { Pace } from './scenes/Pace';
import { Problem } from './scenes/Problem';
import { Product, productDuration } from './scenes/Product';
import { Queue } from './scenes/Queue';
import { AUDIO_OFFSET_FRAMES, bars, color, font } from './theme';

// Every scene boundary is a whole bar of "Launch Day Loop" (97.5 BPM).
export const timeline = () => [
  { id: 'hook', frames: bars(2) },
  { id: 'problem', frames: bars(1.5) },
  { id: 'logo', frames: bars(1.5) },
  { id: 'queue', frames: bars(2) },
  { id: 'pace', frames: bars(3.5) },
  { id: 'product', frames: productDuration() },
  { id: 'features', frames: bars(2) },
  { id: 'outro', frames: bars(2.5) },
];

export const promoDuration = () => timeline().reduce((sum, s) => sum + s.frames, 0);

export type PromoProps = { provider: ProviderId };

export const Promo: React.FC<PromoProps> = ({ provider: id }) => {
  const provider = providers[id];
  const { durationInFrames } = useVideoConfig();
  const scenes: Record<string, React.ReactNode> = {
    hook: <Hook />,
    problem: <Problem />,
    logo: <Logo />,
    queue: <Queue />,
    pace: <Pace provider={provider} />,
    product: <Product provider={provider} />,
    features: <Features provider={provider} />,
    outro: <Outro provider={provider} />,
  };
  let from = 0;
  return (
    <AbsoluteFill style={{ background: color.bg, fontFamily: font.sans }}>
      {timeline().map(scene => {
        const start = from;
        from += scene.frames;
        return (
          <Sequence key={scene.id} from={start} durationInFrames={scene.frames} name={scene.id}>
            <SceneFade duration={scene.frames} fadeIn={scene.id === 'hook' ? 0 : 6} fadeOut={scene.id === 'outro' ? 20 : 6}>
              {scenes[scene.id]}
            </SceneFade>
          </Sequence>
        );
      })}
      <Audio
        src={staticFile('audio/launch-day-loop.mp3')}
        trimBefore={AUDIO_OFFSET_FRAMES}
        volume={f => interpolate(f, [0, 8, durationInFrames - 45, durationInFrames], [0, 0.85, 0.85, 0], { extrapolateLeft: 'clamp', extrapolateRight: 'clamp' })}
      />
    </AbsoluteFill>
  );
};
