import path from 'node:path';
import { mkdirSync } from 'node:fs';
import { expect, openChannel, seed, seedContent, test } from './fixtures';

// Produces the README and docs screenshots: login, channel view and admin
// overview, in dark and light themes, on desktop and phone viewports.
//
// The spec is skipped unless BUBBLES_SCREENSHOTS=1. It starts its own server
// from the e2e fixture, fills it with sample content and writes the PNGs to
// docs/img/ (or BUBBLES_SCREENSHOT_DIR):
//   BUBBLES_SCREENSHOTS=1 npm --prefix web run test:e2e -- screenshots --project=chromium

const enabled = process.env.BUBBLES_SCREENSHOTS === '1';
const outDir = path.resolve(process.env.BUBBLES_SCREENSHOT_DIR ?? path.join('..', 'docs', 'img'));

type Scheme = 'dark' | 'light';
type Form = 'desktop' | 'phone';

const viewports: Record<
  Form,
  {
    viewport: { width: number; height: number };
    isMobile: boolean;
    hasTouch: boolean;
    deviceScaleFactor: number;
  }
> = {
  desktop: {
    viewport: { width: 1280, height: 800 },
    isMobile: false,
    hasTouch: false,
    deviceScaleFactor: 1,
  },
  phone: {
    viewport: { width: 390, height: 844 },
    isMobile: true,
    hasTouch: true,
    deviceScaleFactor: 2,
  },
};

const schemes: Scheme[] = ['dark', 'light'];
const forms: Form[] = ['desktop', 'phone'];

test.use({ specId: 'screenshots' });
test.describe.configure({ mode: 'serial' });

test.describe('documentation screenshots', () => {
  test.skip(!enabled, 'set BUBBLES_SCREENSHOTS=1 to capture docs screenshots');
  test.skip(
    ({ browserName }) => browserName !== 'chromium',
    'screenshots are captured once, in chromium',
  );

  let admin: Awaited<ReturnType<typeof seed>>['admin'];

  test.beforeAll(async ({ server }) => {
    mkdirSync(outDir, { recursive: true });
    const users = await seed(server);
    admin = users.admin;
    await seedContent(server, users.admin, users.bea, users.cy);
  });

  for (const scheme of schemes) {
    for (const form of forms) {
      test(`${scheme} ${form}`, async ({ open }) => {
        const shot = (page: import('@playwright/test').Page, name: string) =>
          page.screenshot({ path: path.join(outDir, `${name}-${scheme}-${form}.png`) });

        // Login is captured before signing in.
        const anon = await open(null, { colorScheme: scheme, ...viewports[form] });
        await anon.page.goto('/login');
        await expect(anon.page.getByLabel('Username')).toBeVisible();
        await shot(anon.page, 'login');

        const { page } = await open(admin, { colorScheme: scheme, ...viewports[form] });
        await page.goto('/');
        if (form === 'phone') {
          await page.getByRole('button', { name: 'Toggle navigation' }).click();
          await page.getByRole('link', { name: /^general/ }).click();
        } else {
          await openChannel(page, 'general');
        }
        await expect(page.getByText('Welcome to the')).toBeVisible();
        await shot(page, 'channel');

        await page.goto('/admin');
        await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible();
        await expect(page.getByText('Open connections', { exact: true })).toBeVisible();
        await shot(page, 'admin-overview');
      });
    }
  }
});
