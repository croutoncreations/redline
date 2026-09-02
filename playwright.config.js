const { defineConfig, devices } = require('@playwright/test');

module.exports = defineConfig({
  testDir: './tests/dashboard',
  timeout: 15_000,
  fullyParallel: true,
  forbidOnly: true,
  reporter: [['line']],
  projects: [
    {
      name: 'desktop',
      testMatch: /dashboard\.spec\.js|screenshots\.spec\.js/,
      use: {
        browserName: 'chromium',
        headless: true,
        viewport: { width: 1280, height: 900 },
      },
    },
    {
      name: 'mobile',
      testMatch: /mobile(?:-screenshots)?\.spec\.js|sw\.spec\.js/,
      use: {
        browserName: 'chromium',
        headless: true,
        // Pixel 9-friendly viewport: 412x915
        viewport: { width: 412, height: 915 },
        userAgent: 'Mozilla/5.0 (Linux; Android 14; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36',
        isMobile: true,
        hasTouch: true,
        deviceScaleFactor: 2.625,
      },
    },
  ],
});
