// Per-cut copy. Numbers mirror the `decision-run` demo scene the captures come
// from (72% weekly left, week resets in 2 days, 43% capacity surplus), so the
// animated explainer and the real UI never disagree.
export type ProviderId = 'generic' | 'claude' | 'codex';

export type ProviderCut = {
  id: ProviderId;
  name: string; // how the community names its tool
  icons: { src: string; invert?: boolean }[];
  captures: 'claude' | 'codex'; // capture filename prefix
  agent: string; // title of the agent chat window
  headline: string;
  windows: string; // what Redline reads for this provider
  others: string;
};

const claudeIcon = { src: 'brand/claude.svg' };
const codexIcon = { src: 'brand/codex.svg', invert: true };

export const providers: Record<ProviderId, ProviderCut> = {
  // Harness-neutral cut for the README, site, HN, and any audience that isn't
  // one tool's community. Uses the Claude captures because they show both the
  // 5-hour and weekly windows, and the dashboard rail shows both providers.
  generic: {
    id: 'generic',
    name: 'Your AI subscription',
    icons: [claudeIcon, codexIcon],
    captures: 'claude',
    agent: 'Your agent',
    headline: 'Never waste your AI subscription quota.',
    windows: 'Reads the same 5-hour and weekly limits your CLIs see',
    others: 'Works with Claude Code, Codex, Pi, and any agent CLI',
  },
  claude: {
    id: 'claude',
    name: 'Claude Code',
    icons: [claudeIcon],
    captures: 'claude',
    agent: 'Claude Code',
    headline: 'Never waste your Claude Code quota.',
    windows: 'Reads your 5-hour window and weekly allowance',
    others: 'Also runs Codex, Pi, and any agent CLI',
  },
  codex: {
    id: 'codex',
    name: 'Codex',
    icons: [codexIcon],
    captures: 'codex',
    agent: 'Codex',
    headline: 'Never waste your Codex quota.',
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
