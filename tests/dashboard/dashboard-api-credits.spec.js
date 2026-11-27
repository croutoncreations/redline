const { test, expect } = require('@playwright/test');
const { loadDashboard, loadMobileDashboard, dashboardFixture, cursorProviderFixture, apiCreditsProviderFixture, apiCreditsNoPromoFixture, profileOptionsFixture } = require('./harness');

// An openai-api-credits account is a dollar balance, not a subscription
// window: it reads in dollars with the promotional grant's expiry, never as a
// weekly, monthly or 5-hour figure. Synthetic fixtures (FIXTURE_NOW + 2d5h
// expiry is 00:00 UTC on Thu, Jul 23, shown as that UTC date even in Los Angeles).

function withCredits(mutate, fixture = apiCreditsProviderFixture) {
  const dashboard = dashboardFixture();
  const credits = fixture();
  if (mutate) mutate(credits);
  dashboard.providers.push(credits);
  return dashboard;
}

function signedOutCredits(credits) {
  credits.account_status = 'signed_out';
  credits.snapshot_stale = true;
  credits.usage_source = { active: 'native', reason: 'signed_out', last_error: 'the OpenAI session key is missing or expired; run `redline account set-session-key credits-main`' };
}

function staleCredits(credits) {
  credits.account_status = 'stale';
  credits.snapshot_stale = true;
  credits.error = 'Usage is stale.';
}

const forbidden = /weekly|monthly|5h|5-hour|No 5h limit|Week resets|Month resets/i;

function staleNotSignedOut(credits) {
  staleCredits(credits);
  credits.snapshot.observed_at = new Date(Date.parse(credits.snapshot.observed_at) - 2 * 3600 * 1000).toISOString();
}

// The promotional balance at a given fraction of its grant.
function promoAt(fraction) {
  return credits => {
    credits.snapshot.credits.promo_remaining = 100 * fraction;
    credits.snapshot.credits.grants[0].remaining = 100 * fraction;
  };
}

// WCAG contrast of an element's text against the first opaque background
// behind it (the element's own, else its nearest ancestor's).
async function textContrast(locator) {
  return locator.evaluate(node => {
    const parse = value => (value.match(/[\d.]+/g) || []).map(Number);
    const luminance = ([r, g, b]) => {
      const channel = c => { c /= 255; return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4; };
      return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
    };
    let background = [255, 255, 255];
    for (let el = node; el; el = el.parentElement) {
      const rgba = parse(getComputedStyle(el).backgroundColor);
      if (rgba.length === 3 || (rgba.length === 4 && rgba[3] === 1)) { background = rgba.slice(0, 3); break; }
    }
    const [a, b] = [luminance(parse(getComputedStyle(node).color).slice(0, 3)), luminance(background)];
    return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
  });
}

const fontSize = locator => locator.evaluate(node => parseFloat(getComputedStyle(node).fontSize));

// Rule 10: Claude, Codex and Cursor render byte-for-byte the same with or
// without a credits account beside them.
async function existingCardsHTML(page, load, withAccount) {
  const dashboard = dashboardFixture();
  dashboard.providers.push(cursorProviderFixture());
  if (withAccount) dashboard.providers.push(apiCreditsProviderFixture());
  await load(page, { dashboard });
  const html = {};
  for (const id of ['claude-main', 'codex-main', 'cursor-main']) {
    const card = page.locator(`[data-provider-id="${id}"]`).first();
    await expect(card).toBeAttached();
    html[id] = await card.evaluate(node => node.innerHTML);
  }
  return html;
}

test.describe('OpenAI API credits account (desktop)', () => {
  test.skip(({ isMobile }) => isMobile, 'desktop dashboard');
  test.use({ timezoneId: 'America/Los_Angeles', locale: 'en-US' });

  test('headlines the promotional balance in dollars with its expiry', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCredits() });
    const card = page.locator('[data-provider-id="credits-main"]');
    await expect(card.locator('.provider-name')).toContainText('OpenAI API credits');
    await expect(card.locator('.provider-name')).toContainText('Linked org');
    await expect(card.locator('.provider-logo')).toHaveText('$');
    await expect(card.locator('.provider-copy-line b')).toHaveText('$62.40 left');
    await expect(card.locator('.provider-window-line')).toHaveText('Expires in 2 days · Thu, Jul 23');
    await expect(card.locator('.compact-progress')).toHaveAttribute('value', '62.4');
    await expect(card.locator('.provider-trigger')).not.toContainText(forbidden);
  });

  test('details show the decision, promotional meter, rules, paid credit and grants in order', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCredits() });
    const card = page.locator('[data-provider-id="credits-main"]');
    await page.getByRole('button', { name: 'Show OpenAI API credits Linked org usage details' }).click();
    const detail = card.locator('.provider-detail');
    await expect(detail.locator('.decision-detail')).toContainText('Run now · firesale');
    await expect(detail.locator('.decision-detail')).toContainText('$62.40 of promotional credit expires in 2 days; running queued work before it lapses');
    await expect(detail.locator('.decision-detail')).not.toContainText('lapses.');
    const promo = detail.getByTestId('credits-promo');
    await expect(promo).toContainText('Promotional credit');
    await expect(promo).toContainText('$62.40 of $100.00 left');
    await expect(promo).toContainText('Expires in 2 days · Thu, Jul 23');
    await expect(promo.locator('progress')).toHaveAttribute('value', '62.4');
    await expect(detail.getByTestId('credits-rules')).toHaveText('Jobs start above $5.00 · Firesale: runs within 3 days of expiry');
    await expect(detail.getByTestId('credits-paid')).toContainText('Other credit');
    await expect(detail.getByTestId('credits-paid')).toContainText('$7.25 · shown only, never scheduled');
    const grants = detail.getByTestId('credits-grant');
    await expect(grants).toHaveCount(2);
    await expect(grants.nth(0)).toContainText('ChatGPT subscription grant');
    await expect(grants.nth(0)).toContainText('$62.40 of $100.00');
    await expect(grants.nth(0)).toContainText('expires Thu, Jul 23');
    await expect(grants.nth(1)).toContainText('Auto recharge');
    await expect(grants.nth(1)).toContainText('$7.25 of $10.00');
    await expect(grants.nth(1)).toContainText('expires Thu, Jun 10, 2027');
    // textContent is document order and doesn't depend on layout, so the
    // check can't race the popover's open animation as innerText can.
    const text = (await detail.textContent()).replace(/\s+/g, ' ');
    const order = ['Run now · firesale', 'Promotional credit', 'Jobs start above', 'Other credit', 'ChatGPT subscription grant'].map(part => text.indexOf(part));
    expect(order.every(index => index >= 0)).toBe(true);
    expect([...order].sort((a, b) => a - b)).toEqual(order);
    await expect(detail.locator('[data-capacity-evidence]')).toHaveCount(0);
    await expect(detail.locator('[data-provider-policy]')).toHaveCount(0);
    await expect(detail).not.toContainText(forbidden);
  });

  test('the rule line, promo expiry and grant expiries are readable small text', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCredits() });
    const card = page.locator('[data-provider-id="credits-main"]');
    await page.getByRole('button', { name: 'Show OpenAI API credits Linked org usage details' }).click();
    const detail = card.locator('.provider-detail');
    const bodySize = await fontSize(detail.getByTestId('credits-promo').locator('.meter-head span'));
    const small = [
      detail.getByTestId('credits-rules'),
      detail.getByTestId('credits-promo').locator('.reset'),
      detail.getByTestId('credits-grant').nth(0).locator('i'),
      detail.getByTestId('credits-grant').nth(1).locator('i'),
    ];
    for (const line of small) {
      const size = await fontSize(line);
      expect(size).toBeGreaterThanOrEqual(11);
      expect(size).toBe(bodySize);
      expect(await textContrast(line)).toBeGreaterThanOrEqual(4.5);
    }
  });

  test('even pacing states its rule', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCredits(credits => { credits.snapshot.credits.pacing = 'even'; }) });
    await page.getByRole('button', { name: 'Show OpenAI API credits Linked org usage details' }).click();
    await expect(page.getByTestId('credits-rules')).toHaveText('Jobs start above $5.00 · Even pacing across the grant');
  });

  test('a stale balance reads as last known, with muted bars', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCredits(staleNotSignedOut) });
    const card = page.locator('[data-provider-id="credits-main"]');
    await expect(card).toHaveClass(/stale/);
    await expect(card.locator('.provider-copy-line b')).toHaveText('Last known $62.40');
    await expect(card.locator('.provider-window-line')).toHaveText('Last sample 2 hrs ago');
    await expect(card.locator('.compact-progress')).toHaveClass(/muted/);
    await expect(card.locator('.compact-progress')).not.toHaveClass(/danger|warn/);
    await expect(card.locator('.provider-trigger')).not.toContainText(forbidden);
    await page.getByRole('button', { name: 'Show OpenAI API credits Linked org usage details' }).click();
    const promo = card.getByTestId('credits-promo');
    await expect(promo).toContainText('Last known promotional credit');
    await expect(promo.locator('progress')).toHaveClass(/muted/);
    await expect(card.locator('.decision-detail')).toContainText('Jobs are held until the balance can be read');
    await expect(card.locator('.decision-detail')).not.toContainText(/Run now|running queued work/);
  });

  test('signed out asks for the session key with the CLI command', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCredits(signedOutCredits) });
    const card = page.locator('[data-provider-id="credits-main"]');
    await expect(card.locator('.provider-copy-line b')).toHaveText('Session key needed');
    await expect(card.locator('.provider-window-line')).toHaveText('Run redline account set-session-key credits-main');
    await expect(card.locator('.provider-pressure')).toHaveText('Session key needed');
    // The name stays readable beside the headline (L1).
    const name = card.locator('.provider-name-text');
    expect(await name.evaluate(node => node.scrollWidth <= node.clientWidth)).toBe(true);
    // No key entry: the only input is the existing parallel-runs limit.
    await expect(card.locator('input:not([data-provider-concurrency]), textarea')).toHaveCount(0);
  });

  test('signed out with a stale RUN decision shows only the fix in details', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCredits(signedOutCredits) });
    const card = page.locator('[data-provider-id="credits-main"]');
    await page.getByRole('button', { name: 'Show OpenAI API credits Linked org usage details' }).click();
    const detail = card.locator('.provider-detail');
    await expect(detail.getByTestId('credits-signed-out')).toHaveText(/Session key needed\s*Run redline account set-session-key credits-main/);
    await expect(detail).not.toContainText(/Run now|running queued work|Last known|Promotional credit|Other credit|Grants/);
    await expect(detail.locator('progress')).toHaveCount(0);
    await expect(detail.getByTestId('credits-promo')).toHaveCount(0);
    await expect(detail.getByTestId('credits-paid')).toHaveCount(0);
    await expect(detail.getByTestId('credits-grant')).toHaveCount(0);
  });

  test('bars are green when healthy, amber below 35% and red below 15%', async ({ page }) => {
    const classes = async (fraction) => {
      await loadDashboard(page, { dashboard: withCredits(promoAt(fraction)) });
      const card = page.locator('[data-provider-id="credits-main"]');
      return [await card.locator('.compact-progress').getAttribute('class'), await card.getByTestId('credits-promo').locator('progress').getAttribute('class')];
    };
    await classes(0.624);
    // The credits bar's colour comes from its own rule, not the shared red accent.
    const fill = await page.evaluate(() => [...document.styleSheets].flatMap(sheet => [...sheet.cssRules])
      .filter(rule => rule.selectorText && /^\.credits-progress(\.\w+)?::-webkit-progress-value$/.test(rule.selectorText))
      .map(rule => [rule.selectorText, rule.style.background || rule.style.backgroundColor]));
    expect(Object.fromEntries(fill)).toEqual({
      '.credits-progress::-webkit-progress-value': 'var(--green)',
      '.credits-progress.warn::-webkit-progress-value': 'var(--amber)',
      '.credits-progress.danger::-webkit-progress-value': 'rgb(255, 123, 114)',
      '.credits-progress.muted::-webkit-progress-value': 'var(--muted)',
    });
    for (const [fraction, tone] of [[0.624, null], [0.36, null], [0.30, 'warn'], [0.16, 'warn'], [0.10, 'danger']]) {
      for (const cls of await classes(fraction)) {
        expect(cls).toMatch(/\bcredits-progress\b/);
        if (tone) expect(cls).toMatch(new RegExp(`\\b${tone}\\b`));
        else expect(cls).not.toMatch(/\b(warn|danger|muted)\b/);
      }
    }
  });

  test('held decisions use the shared wording', async ({ page }) => {
    const cases = [
      ['promotional credit is at or below the $5.00 floor', 'Held · at the floor', 'Promotional credit is at or below the $5.00 floor'],
      ['$2.00 of promotional credit is reserved for 2 run(s) in flight; waiting for one to finish before starting another', 'Held · run in flight', '$2.00 of promotional credit is reserved for 2 run(s) in flight; waiting for one to finish before starting another'],
      ['holding promotional credit until it is within 3 days of expiring', 'Holding credit', 'Holding promotional credit until it is within 3 days of expiring'],
    ];
    for (const [reason, label, detail] of cases) {
      await loadDashboard(page, { dashboard: withCredits(credits => { credits.latest_decision = { decision: 'WAIT', mode: 'api_credits', reason }; }) });
      const card = page.locator('[data-provider-id="credits-main"]');
      await expect(card.locator('.provider-pressure')).toHaveText(label);
      await expect(card.locator('.decision-detail > span')).toHaveText(detail);
    }
    await loadDashboard(page, { dashboard: withCredits(null, apiCreditsNoPromoFixture) });
    await expect(page.locator('[data-provider-id="credits-main"] .provider-pressure')).toHaveText('Held · no promotional credit');
  });

  // A hold is the scheduler working as configured, not an error: grey, never
  // the red of a failure or the amber of a balance that cannot be read.
  test('held decisions read as neutral grey, not red', async ({ page }) => {
    const reasons = [
      'promotional credit is at or below the $5.00 floor',
      '$2.00 of promotional credit is reserved for 2 run(s) in flight; waiting for one to finish before starting another',
      'holding promotional credit until it is within 3 days of expiring',
      'no promotional API credit is available',
    ];
    for (const reason of reasons) {
      await loadDashboard(page, { dashboard: withCredits(credits => { credits.latest_decision = { decision: 'WAIT', mode: 'api_credits', reason }; }) });
      const card = page.locator('[data-provider-id="credits-main"]');
      await expect(card.locator('.provider-pressure')).toHaveCSS('color', 'rgb(184, 190, 194)');
      await expect(card.locator('.decision-detail')).toHaveCSS('border-left-color', 'rgb(89, 99, 107)');
    }
  });

  // Grants lapse at 00:00 UTC; in the Americas the local date is the day
  // before, so the date is the UTC one (test timezone: Los Angeles).
  test('a 00:00 UTC expiry shows its UTC date in Los Angeles', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCredits(credits => {
      credits.snapshot.credits.expires_at = '2026-12-13T00:00:00Z';
      credits.snapshot.credits.grants[0].expires_at = '2026-12-13T00:00:00Z';
    }) });
    const card = page.locator('[data-provider-id="credits-main"]');
    await expect(card.locator('.provider-window-line')).toHaveText(/^Expires in .+ · Sun, Dec 13$/);
    await page.getByRole('button', { name: 'Show OpenAI API credits Linked org usage details' }).click();
    await expect(card.getByTestId('credits-grant').nth(0)).toContainText('expires Sun, Dec 13');
  });

  test('onboarding speaks of the balance, not installs or subscriptions', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCredits() });
    await page.evaluate(() => renderOnboardingAccounts());
    const article = page.locator('#onboarding-accounts .onboarding-account', { hasText: 'OpenAI API credits' });
    await expect(article).toContainText('Balance read from OpenAI');
    await expect(article).not.toContainText(/Install|Subscription usage/);
    await loadDashboard(page, { dashboard: withCredits(signedOutCredits) });
    await page.evaluate(() => renderOnboardingAccounts());
    const out = page.locator('#onboarding-accounts .onboarding-account', { hasText: 'OpenAI API credits' });
    await expect(out).toContainText('redline account set-session-key credits-main');
    await expect(out).toContainText('redline account set-api-key credits-main');
    await expect(out).not.toContainText(/Install|Subscription usage|Balance read/);
  });

  test('no promotional credit is not mistaken for an unreported weekly limit', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCredits(null, apiCreditsNoPromoFixture) });
    const card = page.locator('[data-provider-id="credits-main"]');
    await expect(card.locator('.provider-copy-line b')).toHaveText('$0.00 promo');
    await expect(card.locator('.provider-window-line')).toHaveText('No promotional credit');
    await expect(card.locator('.compact-progress')).toHaveAttribute('value', '0');
    await page.getByRole('button', { name: 'Show OpenAI API credits Linked org usage details' }).click();
    const detail = card.locator('.provider-detail');
    await expect(detail.getByTestId('credits-promo')).toContainText('No promotional credit');
    await expect(detail.getByTestId('credits-paid')).toContainText('$7.25 · shown only, never scheduled');
    await expect(card).not.toContainText(/not reported|weekly/i);
  });

  test('Claude and Codex tiles are unchanged next to a credits account', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCredits() });
    await expect(page.locator('[data-provider-id="claude-main"] .provider-copy-line b')).toHaveText('53% weekly');
    await expect(page.locator('[data-provider-id="codex-main"] .provider-copy-line b')).toHaveText('47% weekly');
    await expect(page.locator('[data-provider-id="codex-main"] .provider-window-line')).toContainText('No 5h limit');
  });

  test('Claude, Codex and Cursor cards are byte-for-byte unchanged by a credits account', async ({ page }) => {
    const without = await existingCardsHTML(page, loadDashboard, false);
    const withAccount = await existingCardsHTML(page, loadDashboard, true);
    expect(withAccount).toEqual(without);
  });

  test('a profile on the credits account offers only pi and a custom command', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCredits() });
    await page.getByRole('button', { name: 'Profiles' }).click();
    await page.locator('#profile-provider').selectOption('credits-main');
    const harnesses = await page.locator('#profile-harness option').evaluateAll(options => options.map(option => option.value));
    expect(harnesses).toEqual(['pi', 'command']);
    await expect(page.locator('#profile-harness')).toHaveValue('pi');
    await page.locator('#profile-provider').selectOption('codex-main');
    const codex = await page.locator('#profile-harness option').evaluateAll(options => options.map(option => option.value));
    expect(codex).toEqual(expect.arrayContaining(['codex-cli', 'claude-code', 'pi', 'command']));
  });

  test('moving an existing Codex profile to the credits account resets it to pi with an openai model', async ({ page }) => {
    const profileOptions = profileOptionsFixture();
    profileOptions.harnesses.find(item => item.id === 'pi').models.codex.push({ id: 'openai/gpt-5.6-sol', label: 'GPT-5.6 Sol (API)', source: 'pi_catalog' });
    await loadDashboard(page, { dashboard: withCredits(), profileOptions });
    await page.getByRole('button', { name: 'Profiles' }).click();
    await page.locator('[data-profile="codex-devx"]').click();
    await expect(page.locator('#profile-harness')).toHaveValue('codex-cli');
    await page.locator('#profile-provider').selectOption('credits-main');
    const harnesses = await page.locator('#profile-harness option').evaluateAll(options => options.map(option => option.value));
    expect(harnesses).toEqual(['pi', 'command']);
    await expect(page.locator('#profile-harness')).toHaveValue('pi');
    await expect(page.locator('#profile-model-choice')).toHaveValue('openai/gpt-5.6-sol');
  });

  test('a credits pi profile defaults to a catalog openai model and never to default', async ({ page }) => {
    const profileOptions = profileOptionsFixture();
    profileOptions.harnesses.find(item => item.id === 'pi').models.codex.push({ id: 'openai/gpt-5.6-sol', label: 'GPT-5.6 Sol (API)', source: 'pi_catalog' });
    const state = await loadDashboard(page, { dashboard: withCredits(), profileOptions });
    await page.getByRole('button', { name: 'Profiles' }).click();
    await page.locator('#profile-id').fill('pi-credits');
    await page.locator('#profile-provider').selectOption('credits-main');
    const models = await page.locator('#profile-model-choice option').evaluateAll(options => options.map(option => option.value));
    expect(models).toEqual(['openai/gpt-5.6-sol', '__other__']);
    await expect(page.locator('#profile-model-choice')).toHaveValue('openai/gpt-5.6-sol');
    await expect(page.locator('#profile-model-hint')).toBeHidden();
    await page.locator('#profile-repository').fill('/repo/redline');
    await page.getByRole('button', { name: 'Save profile' }).click();
    await expect.poll(() => state.requests.filter(item => item.path === '/v1/profiles').length).toBe(1);
    expect(state.requests.find(item => item.path === '/v1/profiles').body).toMatchObject({ provider_account_id: 'credits-main', harness_type: 'pi', model: 'openai/gpt-5.6-sol' });
  });

  test('a credits pi profile without a catalog openai model asks for one before saving', async ({ page }) => {
    const state = await loadDashboard(page, { dashboard: withCredits() });
    await page.getByRole('button', { name: 'Profiles' }).click();
    await page.locator('#profile-id').fill('pi-credits');
    await page.locator('#profile-provider').selectOption('credits-main');
    await expect(page.locator('#profile-harness')).toHaveValue('pi');
    const models = await page.locator('#profile-model-choice option').evaluateAll(options => options.map(option => option.value));
    expect(models).not.toContain('default');
    await expect(page.locator('#profile-model-custom')).toBeVisible();
    await expect(page.locator('#profile-model-custom')).toHaveValue('');
    await expect(page.locator('#profile-model-hint')).toBeVisible();
    await expect(page.locator('#profile-model-hint')).toContainText('openai/');
    await page.locator('#profile-repository').fill('/repo/redline');
    await page.getByRole('button', { name: 'Save profile' }).click();
    await expect(page.locator('#profile-form-error')).toContainText('openai/');
    expect(state.requests.filter(item => item.path === '/v1/profiles')).toHaveLength(0);
    await page.locator('#profile-model-custom').fill('openai/gpt-5.6-sol');
    await expect(page.locator('#profile-model-hint')).toBeHidden();
    await page.getByRole('button', { name: 'Save profile' }).click();
    await expect.poll(() => state.requests.filter(item => item.path === '/v1/profiles').length).toBe(1);
    expect(state.requests.find(item => item.path === '/v1/profiles').body).toMatchObject({ harness_type: 'pi', model: 'openai/gpt-5.6-sol' });
  });
});

test.describe('OpenAI API credits account (mobile)', () => {
  test.skip(({ isMobile }) => !isMobile, 'mobile page');
  test.use({ timezoneId: 'America/Los_Angeles', locale: 'en-US' });

  test('headlines dollars and expiry instead of session and weekly rings', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withCredits() });
    const card = page.locator('[data-provider-id="credits-main"]');
    await expect(card.locator('.m-provider-name')).toHaveText('OpenAI API credits · Linked org');
    await expect(card.locator('.m-provider-logo')).toHaveText('$');
    await expect(card.getByTestId('credits-headline')).toHaveText('$62.40 left');
    await expect(card.getByTestId('credits-secondary')).toHaveText('Expires in 2 days · Thu, Jul 23');
    await expect(card.locator('.m-rings')).toHaveCount(0);
    await expect(card.locator('.m-provider-header')).not.toContainText(forbidden);
    await expect(card.locator('.m-provider-header')).not.toContainText('Session');
  });

  test('details show the promotional meter, rules, paid credit, grants and job controls', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withCredits() });
    const detail = page.getByTestId('provider-detail-credits-main');
    await expect(detail.getByTestId('credits-promo')).toContainText('$62.40 of $100.00 left');
    await expect(detail.getByTestId('credits-promo')).toContainText('Expires in 2 days · Thu, Jul 23');
    await expect(detail.getByTestId('credits-rules')).toHaveText('Jobs start above $5.00 · Firesale: runs within 3 days of expiry');
    await expect(detail.getByTestId('credits-paid')).toHaveText(/^\s*Other credit\s*\$7\.25 · shown only, never scheduled\s*$/);
    await expect(detail.getByTestId('credits-grant')).toHaveCount(2);
    await expect(detail.getByTestId('credits-grant').nth(0)).toContainText('ChatGPT subscription grant');
    await expect(detail.getByTestId('credits-grant').nth(1)).toContainText('Auto recharge');
    await expect(detail.getByRole('button', { name: /View queue/ })).toBeVisible();
    await expect(detail.getByRole('button', { name: /Pause/ })).toBeVisible();
    await expect(detail).not.toContainText(forbidden);
  });

  test('a stale balance reads as last known, muted, without the old decision', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withCredits(staleNotSignedOut) });
    const card = page.locator('[data-provider-id="credits-main"]');
    await expect(card.getByTestId('credits-headline')).toHaveText('Last known $62.40');
    await expect(card.getByTestId('credits-secondary')).toHaveText('Last sample 2 hrs ago');
    const detail = page.getByTestId('provider-detail-credits-main');
    await expect(detail.getByTestId('credits-promo')).toContainText('Last known promotional credit');
    await expect(detail.getByTestId('credits-promo').locator('progress')).toHaveClass(/muted/);
    await expect(detail.getByTestId('decision-detail')).toHaveCount(0);
  });

  test('signed out says to re-paste the key on the computer, with no key entry', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withCredits(signedOutCredits) });
    const card = page.locator('[data-provider-id="credits-main"]');
    await expect(card.getByTestId('credits-headline')).toHaveText('Session key needed');
    await expect(card.getByTestId('credits-secondary')).toHaveText('Session key expired · re-paste it on the computer running Redline');
    // Said once, not again in red in the source line.
    await expect(card.getByText('Session key expired · re-paste it on the computer running Redline')).toHaveCount(1);
    await expect(card).not.toContainText('set-session-key');
    await expect(card.locator('input, textarea')).toHaveCount(0);
  });

  test('signed out with a stale RUN decision shows no reason or meters', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withCredits(signedOutCredits) });
    const card = page.locator('[data-provider-id="credits-main"]');
    const detail = page.getByTestId('provider-detail-credits-main');
    await expect(detail.getByTestId('decision-detail')).toHaveCount(0);
    await expect(detail.getByTestId('credits-promo')).toHaveCount(0);
    await expect(detail.getByTestId('credits-paid')).toHaveCount(0);
    await expect(detail.getByTestId('credits-grant')).toHaveCount(0);
    await expect(card).not.toContainText(/Run now|running queued work|Last known/);
  });

  test('signed out keeps the name on at most two lines and the source line on one', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withCredits(signedOutCredits) });
    const card = page.locator('[data-provider-id="credits-main"]');
    const name = card.locator('.m-provider-name');
    const lines = await name.evaluate(node => Math.round(node.getBoundingClientRect().height / parseFloat(getComputedStyle(node).lineHeight === 'normal' ? 16 : getComputedStyle(node).lineHeight)));
    expect(lines).toBeLessThanOrEqual(2);
    const source = card.locator('.m-source-meta');
    expect((await source.textContent()).replace(/\s+/g, ' ').trim()).toBe('native source · 0/1 active · sampled 1 min ago');
    const height = await source.evaluate(node => node.getBoundingClientRect().height);
    expect(height).toBeLessThan(20);
  });

  test('held decisions use the shared wording, reason capitalised', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withCredits(null, apiCreditsNoPromoFixture) });
    const card = page.locator('[data-provider-id="credits-main"]');
    await expect(card.locator('.m-provider-status')).toHaveText('Held · no promotional credit');
    await expect(card.getByTestId('decision-detail').locator('span')).toHaveText('No promotional API credit is available');
  });

  test('held decisions read as neutral grey, not red', async ({ page }) => {
    const reasons = [
      'promotional credit is at or below the $5.00 floor',
      '$2.00 of promotional credit is reserved for 2 run(s) in flight; waiting for one to finish before starting another',
      'holding promotional credit until it is within 3 days of expiring',
      'no promotional API credit is available',
    ];
    await page.emulateMedia({ colorScheme: 'dark' });
    for (const reason of reasons) {
      await loadMobileDashboard(page, { dashboard: withCredits(credits => { credits.latest_decision = { decision: 'WAIT', mode: 'api_credits', reason }; }) });
      const card = page.locator('[data-provider-id="credits-main"]');
      await expect(card.locator('.m-provider-status')).toHaveCSS('color', 'rgb(145, 152, 158)');
      await expect(card.getByTestId('decision-detail')).toHaveCSS('border-left-color', 'rgb(89, 99, 107)');
    }
  });

  test('a 00:00 UTC expiry shows its UTC date in Los Angeles', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withCredits(credits => {
      credits.snapshot.credits.expires_at = '2026-12-13T00:00:00Z';
      credits.snapshot.credits.grants[0].expires_at = '2026-12-13T00:00:00Z';
    }) });
    const card = page.locator('[data-provider-id="credits-main"]');
    await expect(card.getByTestId('credits-secondary')).toHaveText(/^Expires in .+ · Sun, Dec 13$/);
    await expect(page.getByTestId('provider-detail-credits-main').getByTestId('credits-grant').nth(0)).toContainText('expires Sun, Dec 13');
  });

  test('even pacing states its rule', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withCredits(credits => { credits.snapshot.credits.pacing = 'even'; }) });
    await expect(page.getByTestId('provider-detail-credits-main').getByTestId('credits-rules')).toHaveText('Jobs start above $5.00 · Even pacing across the grant');
  });

  test('Claude, Codex and Cursor cards are byte-for-byte unchanged by a credits account', async ({ page }) => {
    const without = await existingCardsHTML(page, loadMobileDashboard, false);
    const withAccount = await existingCardsHTML(page, loadMobileDashboard, true);
    expect(withAccount).toEqual(without);
  });

  test('no promotional credit is not mistaken for an unreported weekly limit', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withCredits(null, apiCreditsNoPromoFixture) });
    const card = page.locator('[data-provider-id="credits-main"]');
    await expect(card.getByTestId('credits-headline')).toHaveText('$0.00 promo');
    await expect(card.getByTestId('credits-secondary')).toHaveText('No promotional credit');
    await expect(card.getByTestId('credits-paid')).toContainText('$7.25 · shown only, never scheduled');
    await expect(card).not.toContainText(/n\/r|not reported|weekly/i);
  });

  test('two credits accounts share a carousel whose overview shows promo and expiry', async ({ page }) => {
    const dashboard = withCredits();
    const second = apiCreditsNoPromoFixture();
    second.id = 'credits-side'; second.label = 'Side org';
    dashboard.providers.push(second);
    await loadMobileDashboard(page, { dashboard });
    const carousel = page.locator('[data-family="openai-api-credits"]');
    await expect(carousel.locator('.m-provider-name')).toContainText('OpenAI API credits');
    await expect(carousel.locator('.m-acct-columns')).toHaveText(/Promo\s*Expires/);
    const rows = carousel.getByTestId('overview-row');
    await expect(rows.nth(0)).toContainText('$62.40');
    await expect(rows.nth(0)).toContainText('in 2 days');
    await expect(rows.nth(1)).toContainText('$0.00');
    await expect(carousel).not.toContainText(/5-hour|weekly|n\/r/i);
    await rows.nth(0).click();
    await expect(carousel.getByTestId('carousel-page-1').getByTestId('credits-promo')).toContainText('$62.40 of $100.00 left');
  });

  test('a signed-out account in the carousel overview shows no balance', async ({ page }) => {
    const dashboard = withCredits(signedOutCredits);
    const second = apiCreditsNoPromoFixture();
    second.id = 'credits-side'; second.label = 'Side org';
    dashboard.providers.push(second);
    await loadMobileDashboard(page, { dashboard });
    const row = page.locator('[data-family="openai-api-credits"]').getByTestId('overview-row').nth(0);
    await expect(row).toContainText('Session key needed');
    await expect(row).not.toContainText('$62.40');
    await expect(row.locator('.m-acct-cell').first()).toHaveClass(/muted/);
  });

  test('a stale account in the carousel overview shows its last-known balance muted, with no expiry', async ({ page }) => {
    const dashboard = withCredits(staleNotSignedOut);
    const second = apiCreditsNoPromoFixture();
    second.id = 'credits-side'; second.label = 'Side org';
    dashboard.providers.push(second);
    await loadMobileDashboard(page, { dashboard });
    const row = page.locator('[data-family="openai-api-credits"]').getByTestId('overview-row').nth(0);
    await expect(row).toContainText('Out of date');
    const [promo, expires] = [row.locator('.m-acct-cell').nth(0), row.locator('.m-acct-cell').nth(1)];
    await expect(promo.locator('b')).toHaveText('$62.40');
    await expect(promo).toHaveClass(/\bmuted\b/);
    // A last-known balance makes no claim about when it lapses.
    await expect(expires.locator('b')).toHaveText('—');
    await expect(expires).toHaveClass(/\bmuted\b/);
    await expect(row).not.toContainText(/in 2 days|expires|Thu, Jul 23/);
    await expect(row).toHaveAttribute('aria-label', /Last known \$62\.40/);
  });

  test('the Run now status is AA-readable in light mode and unchanged in dark mode', async ({ page }) => {
    await page.emulateMedia({ colorScheme: 'light' });
    await loadMobileDashboard(page, { dashboard: withCredits() });
    const status = page.locator('[data-provider-id="credits-main"] .m-provider-status');
    await expect(status).toHaveClass(/\btriggered\b/);
    expect(await textContrast(status)).toBeGreaterThanOrEqual(4.5);
    await page.emulateMedia({ colorScheme: 'dark' });
    await expect(status).toHaveCSS('color', 'rgb(255, 145, 139)');
  });

  test('the credits card controls have 44px hit areas', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withCredits() });
    const card = page.locator('[data-provider-id="credits-main"]');
    const controls = [
      card.locator('[data-provider-toggle="credits-main"]'),
      card.getByTestId('decision-detail').locator('summary'),
      card.getByRole('button', { name: /View queue/ }),
      card.getByRole('button', { name: /Pause/ }),
      card.getByRole('button', { name: /Refresh usage/ }),
    ];
    for (const control of controls) {
      const box = await control.boundingBox();
      expect(box, await control.evaluate(node => node.outerHTML.slice(0, 80))).not.toBeNull();
      expect(box.width).toBeGreaterThanOrEqual(44);
      expect(box.height).toBeGreaterThanOrEqual(44);
    }
  });

  test('the credits card reads clearly in light mode', async ({ page }) => {
    await page.emulateMedia({ colorScheme: 'light' });
    await loadMobileDashboard(page, { dashboard: withCredits() });
    const card = page.locator('[data-provider-id="credits-main"]');
    await expect(card.getByTestId('credits-headline')).toBeVisible();
    await expect(card.getByTestId('credits-promo').locator('progress')).toBeVisible();
    await expect(card.getByTestId('credits-paid')).toBeVisible();
    await card.getByTestId('decision-detail').locator('summary').click();
    // Every element that draws its own text, header to refresh button.
    const texts = card.locator('*').filter({ visible: true });
    const failures = [];
    for (const node of await texts.all()) {
      const own = await node.evaluate(el => [...el.childNodes].some(child => child.nodeType === Node.TEXT_NODE && child.textContent.trim()));
      if (!own) continue;
      const ratio = await textContrast(node);
      if (ratio < 4.5) failures.push(`${await node.evaluate(el => `${el.tagName.toLowerCase()}.${el.className}`)} "${(await node.textContent()).trim().slice(0, 40)}" ${ratio.toFixed(2)}`);
    }
    expect(failures).toEqual([]);
  });

  test('the queue picker names the credits account', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withCredits() });
    await expect(page.locator('#m-queue-provider option[value="credits-main"]')).toHaveText('credits-main · OpenAI API credits');
    await expect(page.locator('#m-queue-provider option[value="codex-main"]')).toHaveText('codex-main · codex');
  });
});
