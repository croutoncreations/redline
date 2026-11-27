const { test, expect } = require('@playwright/test');
const { loadDashboard, loadMobileDashboard, dashboardFixture, apiCreditsProviderFixture, claudeCreditsProviderFixture, profileOptionsFixture } = require('./harness');

// An anthropic-api-credits account reuses the credits card everywhere; only
// the vendor wording changes: "Claude API credits", the Claude artwork, and a
// signed-out fix that names platform.claude.com. Synthetic fixtures
// (FIXTURE_NOW + 2d5h expiry is 00:00 UTC on Thu, Jul 23, shown as that UTC date even in Los Angeles).

function withClaudeCredits(mutate) {
  const dashboard = dashboardFixture();
  const credits = claudeCreditsProviderFixture();
  if (mutate) mutate(credits);
  dashboard.providers.push(credits);
  return dashboard;
}

function signedOut(credits) {
  credits.account_status = 'signed_out';
  credits.snapshot_stale = true;
  credits.usage_source = { active: 'native', reason: 'signed_out', last_error: 'Claude Console rejected the saved session key' };
}

const forbidden = /weekly|monthly|5h|5-hour|No 5h limit|Week resets|Month resets/i;

function withClaudePiModels() {
  const options = profileOptionsFixture();
  options.harnesses.find(item => item.id === 'pi').models.claude.push({ id: 'anthropic/claude-sonnet-4-6', label: 'Claude Sonnet 4.6 (API)', source: 'pi_catalog' });
  return options;
}

test.describe('Claude API credits account (desktop)', () => {
  test.skip(({ isMobile }) => isMobile, 'desktop dashboard');
  test.use({ timezoneId: 'America/Los_Angeles', locale: 'en-US' });

  test('headlines the balance as Claude API credits with the Claude artwork', async ({ page }) => {
    await loadDashboard(page, { dashboard: withClaudeCredits() });
    const card = page.locator('[data-provider-id="claude-credits"]');
    await expect(card.locator('.provider-name')).toContainText('Claude API credits');
    await expect(card.locator('.provider-name')).toContainText('Console org');
    await expect(card.locator('.provider-logo img')).toHaveAttribute('src', '/assets/claude.svg');
    await expect(card.locator('.provider-copy-line b')).toHaveText('$62.40 left');
    await expect(card.locator('.provider-window-line')).toHaveText('Expires in 2 days · Thu, Jul 23');
    await expect(card.locator('.provider-trigger')).not.toContainText(forbidden);
  });

  test('details name the subscription credit and purchased credit', async ({ page }) => {
    await loadDashboard(page, { dashboard: withClaudeCredits() });
    const card = page.locator('[data-provider-id="claude-credits"]');
    await page.getByRole('button', { name: 'Show Claude API credits Console org usage details' }).click();
    const detail = card.locator('.provider-detail');
    await expect(detail.getByTestId('credits-promo')).toContainText('$62.40 of $100.00 left');
    await expect(detail.getByTestId('credits-paid')).toContainText('$7.25 · shown only, never scheduled');
    await expect(detail.getByTestId('credits-grant').nth(0)).toContainText('Claude subscription credit');
    await expect(detail.getByTestId('credits-grant').nth(1)).toContainText('Purchased credit');
    await expect(detail).not.toContainText(/ChatGPT|OpenAI/);
  });

  test('signed out names platform.claude.com and the CLI command', async ({ page }) => {
    await loadDashboard(page, { dashboard: withClaudeCredits(signedOut) });
    const card = page.locator('[data-provider-id="claude-credits"]');
    await expect(card.locator('.provider-copy-line b')).toHaveText('Session key needed');
    await expect(card.locator('.provider-window-line')).toHaveText('Paste the sessionKey cookie from platform.claude.com: redline account set-session-key claude-credits');
    await page.getByRole('button', { name: 'Show Claude API credits Console org usage details' }).click();
    await expect(card.getByTestId('credits-signed-out')).toContainText('platform.claude.com');
    await expect(card.locator('input:not([data-provider-concurrency]), textarea')).toHaveCount(0);
  });

  test('onboarding reads the balance from the Claude Console', async ({ page }) => {
    await loadDashboard(page, { dashboard: withClaudeCredits() });
    await page.evaluate(() => renderOnboardingAccounts());
    const article = page.locator('#onboarding-accounts .onboarding-account', { hasText: 'Claude API credits' });
    await expect(article).toContainText('Balance read from the Claude Console');
    await expect(article).toContainText('redline account set-api-key claude-credits');
    await expect(article).not.toContainText(/OpenAI|Install|Subscription usage/);
  });

  test('the OpenAI credits card keeps its wording beside a Claude one', async ({ page }) => {
    const dashboard = withClaudeCredits();
    dashboard.providers.push(apiCreditsProviderFixture());
    await loadDashboard(page, { dashboard });
    const openai = page.locator('[data-provider-id="credits-main"]');
    await expect(openai.locator('.provider-name')).toContainText('OpenAI API credits');
    await expect(openai.locator('.provider-logo')).toHaveText('$');
  });

  test('a profile on the Claude credits account offers claude-code, pi and a custom command', async ({ page }) => {
    await loadDashboard(page, { dashboard: withClaudeCredits() });
    await page.getByRole('button', { name: 'Profiles' }).click();
    await page.locator('#profile-provider').selectOption('claude-credits');
    const harnesses = await page.locator('#profile-harness option').evaluateAll(options => options.map(option => option.value));
    expect(harnesses).toEqual(['claude-code', 'pi', 'command']);
    await expect(page.locator('#profile-harness')).toHaveValue('claude-code');
  });

  test('moving a Codex profile to the Claude credits account resets it to claude-code', async ({ page }) => {
    await loadDashboard(page, { dashboard: withClaudeCredits() });
    await page.getByRole('button', { name: 'Profiles' }).click();
    await page.locator('[data-profile="codex-devx"]').click();
    await expect(page.locator('#profile-harness')).toHaveValue('codex-cli');
    await page.locator('#profile-provider').selectOption('claude-credits');
    await expect(page.locator('#profile-harness')).toHaveValue('claude-code');
  });

  test('a Claude credits pi profile defaults to a catalog anthropic model and never to default', async ({ page }) => {
    const state = await loadDashboard(page, { dashboard: withClaudeCredits(), profileOptions: withClaudePiModels() });
    await page.getByRole('button', { name: 'Profiles' }).click();
    await page.locator('#profile-id').fill('pi-claude-credits');
    await page.locator('#profile-provider').selectOption('claude-credits');
    await page.locator('#profile-harness').selectOption('pi');
    const models = await page.locator('#profile-model-choice option').evaluateAll(options => options.map(option => option.value));
    expect(models).toEqual(['anthropic/claude-sonnet-4-6', '__other__']);
    await expect(page.locator('#profile-model-choice')).toHaveValue('anthropic/claude-sonnet-4-6');
    await expect(page.locator('#profile-model-hint')).toBeHidden();
    await page.locator('#profile-repository').fill('/repo/redline');
    await page.getByRole('button', { name: 'Save profile' }).click();
    await expect.poll(() => state.requests.filter(item => item.path === '/v1/profiles').length).toBe(1);
    expect(state.requests.find(item => item.path === '/v1/profiles').body).toMatchObject({ provider_account_id: 'claude-credits', harness_type: 'pi', model: 'anthropic/claude-sonnet-4-6' });
  });

  test('a Claude credits pi profile without an anthropic model asks for one before saving', async ({ page }) => {
    const state = await loadDashboard(page, { dashboard: withClaudeCredits() });
    await page.getByRole('button', { name: 'Profiles' }).click();
    await page.locator('#profile-id').fill('pi-claude-credits');
    await page.locator('#profile-provider').selectOption('claude-credits');
    await page.locator('#profile-harness').selectOption('pi');
    const models = await page.locator('#profile-model-choice option').evaluateAll(options => options.map(option => option.value));
    expect(models).not.toContain('default');
    expect(models).not.toContain('anthropic-cli/claude-fable-5');
    await expect(page.locator('#profile-model-hint')).toBeVisible();
    await expect(page.locator('#profile-model-hint')).toContainText('anthropic/');
    await page.locator('#profile-repository').fill('/repo/redline');
    await page.getByRole('button', { name: 'Save profile' }).click();
    await expect(page.locator('#profile-form-error')).toContainText('anthropic/');
    expect(state.requests.filter(item => item.path === '/v1/profiles')).toHaveLength(0);
    await page.locator('#profile-model-custom').fill('anthropic/claude-sonnet-4-6');
    await expect(page.locator('#profile-model-hint')).toBeHidden();
    await page.getByRole('button', { name: 'Save profile' }).click();
    await expect.poll(() => state.requests.filter(item => item.path === '/v1/profiles').length).toBe(1);
  });
});

test.describe('Claude API credits account (mobile)', () => {
  test.skip(({ isMobile }) => !isMobile, 'mobile page');
  test.use({ timezoneId: 'America/Los_Angeles', locale: 'en-US' });

  test('headlines dollars as Claude API credits with the Claude artwork', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withClaudeCredits() });
    const card = page.locator('[data-provider-id="claude-credits"]');
    await expect(card.locator('.m-provider-name')).toHaveText('Claude API credits · Console org');
    await expect(card.locator('.m-provider-logo img')).toHaveAttribute('src', '/assets/claude.svg');
    await expect(card.getByTestId('credits-headline')).toHaveText('$62.40 left');
    await expect(card.getByTestId('credits-secondary')).toHaveText('Expires in 2 days · Thu, Jul 23');
    await expect(card.locator('.m-rings')).toHaveCount(0);
  });

  test('details name the subscription credit', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withClaudeCredits() });
    const detail = page.getByTestId('provider-detail-claude-credits');
    await expect(detail.getByTestId('credits-grant').nth(0)).toContainText('Claude subscription credit');
    await expect(detail.getByTestId('credits-grant').nth(1)).toContainText('Purchased credit');
    await expect(detail).not.toContainText(/ChatGPT|OpenAI|weekly/i);
  });

  test('signed out names platform.claude.com, with no key entry', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withClaudeCredits(signedOut) });
    const card = page.locator('[data-provider-id="claude-credits"]');
    await expect(card.getByTestId('credits-headline')).toHaveText('Session key needed');
    await expect(card.getByTestId('credits-secondary')).toHaveText('Session key expired · re-paste it from platform.claude.com on the computer running Redline');
    await expect(card).not.toContainText('set-session-key');
    await expect(card.locator('input, textarea')).toHaveCount(0);
  });

  test('the queue picker names the Claude credits account', async ({ page }) => {
    await loadMobileDashboard(page, { dashboard: withClaudeCredits() });
    await expect(page.locator('#m-queue-provider option[value="claude-credits"]')).toHaveText('claude-credits · Claude API credits');
  });

  test('two Claude credits accounts share a carousel with promo and expiry columns', async ({ page }) => {
    const dashboard = withClaudeCredits();
    const second = claudeCreditsProviderFixture();
    second.id = 'claude-credits-side'; second.label = 'Side org';
    dashboard.providers.push(second);
    await loadMobileDashboard(page, { dashboard });
    const carousel = page.locator('[data-family="anthropic-api-credits"]');
    await expect(carousel.locator('.m-provider-name')).toContainText('Claude API credits');
    await expect(carousel.locator('.m-acct-columns')).toHaveText(/Promo\s*Expires/);
    await expect(carousel.locator('.m-provider-logo img')).toHaveAttribute('src', '/assets/claude.svg');
  });
});
