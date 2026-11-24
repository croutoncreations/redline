const { test, expect } = require('@playwright/test');
const { loadDashboard, loadMobileDashboard, dashboardFixture, apiCreditsProviderFixture, apiCreditsNoPromoFixture } = require('./harness');

// An openai-api-credits account is a dollar balance, not a subscription
// window: it reads in dollars with the promotional grant's expiry, never as a
// weekly, monthly or 5-hour figure. Synthetic fixtures (FIXTURE_NOW + 2d5h
// expiry is Wed, Jul 22 in Los Angeles).

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
    await expect(card.locator('.provider-window-line')).toHaveText('Expires in 2 days · Wed, Jul 22');
    await expect(card.locator('.compact-progress')).toHaveAttribute('value', '62.4');
    await expect(card.locator('.provider-trigger')).not.toContainText(forbidden);
  });

  test('details show the decision, promotional meter, rules, paid credit and grants in order', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCredits() });
    const card = page.locator('[data-provider-id="credits-main"]');
    await page.getByRole('button', { name: 'Show OpenAI API credits Linked org usage details' }).click();
    const detail = card.locator('.provider-detail');
    await expect(detail.locator('.decision-detail')).toContainText('Run now · firesale');
    await expect(detail.locator('.decision-detail')).toContainText('$62.40 of promotional credit expires in 2 days');
    const promo = detail.getByTestId('credits-promo');
    await expect(promo).toContainText('Promotional credit');
    await expect(promo).toContainText('$62.40 of $100.00 left');
    await expect(promo).toContainText('Expires in 2 days · Wed, Jul 22');
    await expect(promo.locator('progress')).toHaveAttribute('value', '62.4');
    await expect(detail.getByTestId('credits-rules')).toHaveText('Jobs start above $5.00 · Firesale: runs within 3 days of expiry');
    await expect(detail.getByTestId('credits-paid')).toContainText('Paid credit');
    await expect(detail.getByTestId('credits-paid')).toContainText('$7.25 · shown only, never scheduled');
    const grants = detail.getByTestId('credits-grant');
    await expect(grants).toHaveCount(2);
    await expect(grants.nth(0)).toContainText('ChatGPT subscription grant');
    await expect(grants.nth(0)).toContainText('$62.40 of $100.00');
    await expect(grants.nth(0)).toContainText('expires Wed, Jul 22');
    await expect(grants.nth(1)).toContainText('Auto recharge');
    await expect(grants.nth(1)).toContainText('$7.25 of $10.00');
    await expect(grants.nth(1)).toContainText('expires Thu, Jun 10, 2027');
    const text = await detail.innerText();
    const order = ['Run now · firesale', 'Promotional credit', 'Jobs start above', 'Paid credit', 'ChatGPT subscription grant'].map(part => text.indexOf(part));
    expect(order.every(index => index >= 0)).toBe(true);
    expect([...order].sort((a, b) => a - b)).toEqual(order);
    await expect(detail.locator('[data-capacity-evidence]')).toHaveCount(0);
    await expect(detail.locator('[data-provider-policy]')).toHaveCount(0);
    await expect(detail).not.toContainText(forbidden);
  });

  test('even pacing states its rule', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCredits(credits => { credits.snapshot.credits.pacing = 'even'; }) });
    await page.getByRole('button', { name: 'Show OpenAI API credits Linked org usage details' }).click();
    await expect(page.getByTestId('credits-rules')).toHaveText('Jobs start above $5.00 · Even pacing across the grant');
  });

  test('a stale balance reads as last known', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCredits(staleCredits) });
    const card = page.locator('[data-provider-id="credits-main"]');
    await expect(card).toHaveClass(/stale/);
    await expect(card.locator('.provider-copy-line b')).toHaveText('Last known $62.40');
    await expect(card.locator('.provider-trigger')).not.toContainText(forbidden);
  });

  test('signed out asks for the session key with the CLI command', async ({ page }) => {
    await loadDashboard(page, { dashboard: withCredits(signedOutCredits) });
    const card = page.locator('[data-provider-id="credits-main"]');
    await expect(card.locator('.provider-copy-line b')).toHaveText('Session key needed');
    await expect(card.locator('.provider-window-line')).toHaveText('Run redline account set-session-key credits-main');
    await expect(card.locator('.provider-pressure')).toHaveText('Session key needed');
    // No key entry: the only input is the existing parallel-runs limit.
    await expect(card.locator('input:not([data-provider-concurrency]), textarea')).toHaveCount(0);
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
    await expect(card.getByTestId('credits-secondary')).toHaveText('Expires in 2 days · Wed, Jul 22');
    await expect(card.locator('.m-rings')).toHaveCount(0);
    await expect(card.locator('.m-provider-header')).not.toContainText(forbidden);
    await expect(card.locator('.m-provider-header')).not.toContainText('Session');
  });

  test('details show the promotional meter, rules, paid credit, grants and job controls', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withCredits() });
    const detail = page.getByTestId('provider-detail-credits-main');
    await expect(detail.getByTestId('credits-promo')).toContainText('$62.40 of $100.00 left');
    await expect(detail.getByTestId('credits-promo')).toContainText('Expires in 2 days · Wed, Jul 22');
    await expect(detail.getByTestId('credits-rules')).toHaveText('Jobs start above $5.00 · Firesale: runs within 3 days of expiry');
    await expect(detail.getByTestId('credits-paid')).toContainText('$7.25 · shown only, never scheduled');
    await expect(detail.getByTestId('credits-grant')).toHaveCount(2);
    await expect(detail.getByTestId('credits-grant').nth(0)).toContainText('ChatGPT subscription grant');
    await expect(detail.getByTestId('credits-grant').nth(1)).toContainText('Auto recharge');
    await expect(detail.getByRole('button', { name: /View queue/ })).toBeVisible();
    await expect(detail.getByRole('button', { name: /Pause/ })).toBeVisible();
    await expect(detail).not.toContainText(forbidden);
  });

  test('a stale balance reads as last known', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withCredits(staleCredits) });
    const card = page.locator('[data-provider-id="credits-main"]');
    await expect(card.getByTestId('credits-headline')).toHaveText('Last known $62.40');
  });

  test('signed out says to re-paste the key on the computer, with no key entry', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withCredits(signedOutCredits) });
    const card = page.locator('[data-provider-id="credits-main"]');
    await expect(card.getByTestId('credits-headline')).toHaveText('Session key needed');
    await expect(card.getByTestId('credits-secondary')).toHaveText('Session key expired · re-paste it on the computer running Redline');
    await expect(card.getByTestId('signed-out')).toHaveText('Session key expired · re-paste it on the computer running Redline');
    await expect(card).not.toContainText('set-session-key');
    await expect(card.locator('input, textarea')).toHaveCount(0);
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

  test('the queue picker names the credits account', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withCredits() });
    await expect(page.locator('#m-queue-provider option[value="credits-main"]')).toHaveText('credits-main · OpenAI API credits');
    await expect(page.locator('#m-queue-provider option[value="codex-main"]')).toHaveText('codex-main · codex');
  });
});
