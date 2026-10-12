import { defineConfig, devices } from '@playwright/test';

// End-to-end specs live in tests/e2e. Each spec file starts its own bubbles
// server (built with -tags e2e) on random ports; see tests/e2e/fixtures.
// Chromium trusts the server through an SPKI pin so service workers run; the
// WebKit and Firefox projects ignore certificate errors and skip those specs.
export default defineConfig({
  testDir: './tests/e2e',
  globalSetup: './tests/e2e/fixtures/global-setup.ts',
  fullyParallel: false,
  // Each worker runs a full server plus a browser. On a Windows dev machine
  // WebKit and Firefox hit loopback connect timeouts when several run in
  // parallel, so local runs are serial. One retry keeps a genuine failure
  // visible: Playwright reports retried passes as "flaky", not as passed.
  workers: process.env.CI ? 2 : 1,
  retries: 1,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: process.env.CI ? [['dot'], ['github']] : [['dot']],
  use: {
    trace: 'on-first-retry',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
    { name: 'webkit', use: { ...devices['Desktop Safari'], ignoreHTTPSErrors: true } },
    { name: 'firefox', use: { ...devices['Desktop Firefox'], ignoreHTTPSErrors: true } },
  ],
});
