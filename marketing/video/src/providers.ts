// Per-cut copy. Numbers mirror the `decision-run` demo scene the captures come
// from (72% weekly left, week resets in 2 days, 43% capacity surplus), so the
// animated explainer and the real UI never disagree.
export type ProviderId = 'claude' | 'codex';

export type ProviderCut = {
  id: ProviderId;
  name: string; // how the community names its tool
  icon: string;
  captures: string; // capture filename prefix
  headline: string;
  windows: string; // what Redline reads for this provider
  others: string;
};

export const providers: Record<ProviderId, ProviderCut> = {
  claude: {
    id: 'claude',
    name: 'Claude Code',
    icon: 'brand/claude.svg',
    captures: 'claude',
    headline: 'Never waste your Claude Code quota.',
    windows: 'Reads your 5-hour window and weekly allowance',
    others: 'Also runs Codex, Pi, and any agent CLI',
  },
  codex: {
    id: 'codex',
    name: 'Codex',
    icon: 'brand/codex.svg',
    captures: 'codex',
    headline: 'Never waste your Codex subscription.',
    windows: 'Reads your weekly allowance',
    others: 'Also runs Claude Code, Pi, and any agent CLI',
  },
};

export const pace = {
  weeklyLeft: 72,
  daysElapsed: 5,
  daysInWeek: 7,
  surplus: 43, // demo evaluator's reported pace gap
  job: 'Find and fix one real bug',
};
