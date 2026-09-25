import React from 'react';
import { AbsoluteFill, Sequence, useCurrentFrame } from 'remotion';
import { Caption, CaptureShot } from '../components/CaptureShot';
import { progress, useLayout } from '../components/primitives';
import { ProviderCut } from '../providers';
import { beats } from '../theme';

// Real dashboard, captured from `redline demo serve`: the capacity explainer,
// the same job flipping to RUNNING, then its finished result.
export const PRODUCT_BEATS = { capacity: 8, running: 6, completed: 6, detail: 6 };
export const productDuration = () => beats(Object.values(PRODUCT_BEATS).reduce((a, b) => a + b, 0));

const Flash: React.FC = () => {
  const frame = useCurrentFrame();
  const o = 1 - progress(frame, 0, 8);
  return <AbsoluteFill style={{ background: '#fff', opacity: o * 0.14, pointerEvents: 'none' }} />;
};

export const Product: React.FC<{ provider: ProviderCut }> = ({ provider }) => {
  const { square } = useLayout();
  const c = provider.captures;
  const b = PRODUCT_BEATS;
  let at = 0;
  const seq = (n: number) => {
    const from = at;
    at += beats(n);
    return { from, durationInFrames: beats(n) };
  };
  const s1 = seq(b.capacity);
  const s2 = seq(b.running);
  const s3 = seq(b.completed);
  const s4 = seq(b.detail);

  return (
    <AbsoluteFill>
      <Sequence {...s1}>
        <CaptureShot
          shot={`${c}-02-capacity`}
          keys={[
            { at: 0, box: 'main', pad: 0 },
            { at: 10, box: 'popover', pad: 40, duration: 34 },
            { at: beats(4.5), box: 'decisionDetail', pad: 70, duration: 30 },
          ]}
          rings={[{ box: 'decisionDetail', at: beats(5), pad: 6 }]}
        >
          <Caption title="It checks your real allowance." sub={provider.windows} at={beats(1)} />
        </CaptureShot>
      </Sequence>
      <Sequence {...s2}>
        <CaptureShot
          shot={`${c}-03-running`}
          keys={
            square
              ? [
                  { at: 0, box: 'row', pad: 20 },
                  { at: beats(1), box: 'status', pad: 150, duration: 30 },
                ]
              : [
                  { at: 0, box: 'row', pad: 40 },
                  { at: beats(3), box: 'queue', pad: 30, duration: 30 },
                ]
          }
          rings={[{ box: 'status', at: 6, pad: 14 }]}
        >
          <Caption title="Spare capacity? The job starts." sub="Your coding agent, in an isolated Git worktree" at={8} />
        </CaptureShot>
        <Flash />
      </Sequence>
      <Sequence {...s3}>
        <CaptureShot
          shot={`${c}-04-completed`}
          keys={[
            { at: 0, box: 'main', pad: 0 },
            { at: 6, box: square ? 'firstRun' : 'activity', pad: square ? 24 : 30, duration: 30 },
          ]}
          rings={[{ box: 'firstRun', at: beats(1.5), pad: 10 }]}
        >
          <Caption title="You come back to finished work." sub="Results, artifacts, and a draft PR waiting for review" at={beats(1)} />
        </CaptureShot>
      </Sequence>
      <Sequence {...s4}>
        <CaptureShot
          shot={`${c}-05-run-detail`}
          keys={[
            { at: 0, box: 'dialog', pad: 20 },
            { at: beats(2), box: 'runResult', pad: 80, duration: 34 },
          ]}
        >
          <Caption title="Every decision is explained." sub="Why it ran, what it did, and the full logs" at={beats(1)} />
        </CaptureShot>
      </Sequence>
    </AbsoluteFill>
  );
};
