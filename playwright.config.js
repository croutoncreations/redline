const { defineConfig } = require('@playwright/test');

module.exports = defineConfig({
  testDir: './tests/dashboard',
  timeout: 15_000,
  fullyParallel: true,
  forbidOnly: true,
  reporter: [['line']],
  use: {
    browserName: 'chromium',
    headless: true,
    viewport: { width: 1280, height: 900 },
  },
});
