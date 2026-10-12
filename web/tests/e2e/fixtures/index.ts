import AxeBuilder from '@axe-core/playwright';
import {
  test as base,
  expect,
  type Browser,
  type BrowserContext,
  type Page,
} from '@playwright/test';
import { Server, SESSION_COOKIE, type User } from './server';

export { expect, Server, type Page, type User };
export { PASSWORD, SETUP_TOKEN } from './server';
export { buildJpegWithGps, hasGpsMetadata } from './jpeg';

export interface OpenOptions {
  viewport?: { width: number; height: number };
  colorScheme?: 'dark' | 'light';
  hasTouch?: boolean;
  isMobile?: boolean;
  deviceScaleFactor?: number;
  reducedMotion?: 'reduce' | 'no-preference';
}

export interface Session {
  context: BrowserContext;
  page: Page;
}

interface TestFixtures {
  /** Opens a new browser context signed in as user (or signed out when null). */
  open: (user: User | null, opts?: OpenOptions) => Promise<Session>;
}

interface WorkerFixtures {
  /** Set with test.use({ specId }) so every spec file gets its own server. */
  specId: string;
  server: Server;
}

export const test = base.extend<TestFixtures, WorkerFixtures>({
  specId: ['', { option: true, scope: 'worker' }],

  server: [
    async ({ specId }, use) => {
      const server = await Server.start(specId || 'spec');
      await use(server);
      await server.stop();
    },
    { scope: 'worker', timeout: 120_000 },
  ],

  baseURL: async ({ server }, use) => {
    await use(server.origin);
  },

  // Chromium trusts the server's leaf through its SPKI hash, so service
  // workers and push see a valid certificate. The other engines have no such
  // switch; their projects set ignoreHTTPSErrors and skip service worker specs.
  browser: [
    async ({ playwright, browserName, server, launchOptions }, use) => {
      const args = [...(launchOptions.args ?? [])];
      if (browserName === 'chromium') {
        args.push(`--ignore-certificate-errors-spki-list=${server.spki}`);
      }
      const browser: Browser = await playwright[browserName].launch({ ...launchOptions, args });
      await use(browser);
      await browser.close();
    },
    { scope: 'worker', timeout: 60_000 },
  ],

  open: async ({ browser, server, browserName }, use) => {
    const opened: BrowserContext[] = [];
    await use(async (user, opts = {}) => {
      const context = await browser.newContext({
        baseURL: server.origin,
        ignoreHTTPSErrors: browserName !== 'chromium',
        // Only Chromium (SPKI pin) can run a service worker against this certificate.
        serviceWorkers: browserName === 'chromium' ? 'allow' : 'block',
        viewport: opts.viewport ?? { width: 1280, height: 800 },
        colorScheme: opts.colorScheme ?? 'dark',
        reducedMotion: opts.reducedMotion ?? 'reduce',
        hasTouch: opts.hasTouch,
        isMobile: opts.isMobile,
        deviceScaleFactor: opts.deviceScaleFactor,
        permissions: [],
      });
      opened.push(context);
      if (user) await signIn(context, server, user);
      const page = await context.newPage();
      return { context, page };
    });
    for (const c of opened) await c.close().catch(() => undefined);
  },
});

/** Puts the user's session cookie into a context without going through the form. */
export async function signIn(context: BrowserContext, server: Server, user: User): Promise<void> {
  await context.addCookies([
    { name: SESSION_COOKIE, value: user.cookie, url: server.origin, httpOnly: true, secure: true },
  ]);
}

/** Signs in through the login form. */
export async function loginAs(page: Page, user: { username: string; password: string }) {
  await page.goto('/login');
  await page.getByLabel('Username').fill(user.username);
  await page.getByLabel('Password').fill(user.password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).not.toHaveURL(/\/login/);
}

/** Runs the first-run wizard in the browser. */
export async function setupAdminUI(
  page: Page,
  token: string,
  opts: { instance: string; username: string; password: string },
) {
  await page.goto('/setup');
  await page.getByLabel('Setup token').fill(token);
  await page.getByLabel('Instance name').fill(opts.instance);
  await page.getByLabel('Username').fill(opts.username);
  await page.getByLabel('Password').fill(opts.password);
  await page.getByRole('button', { name: 'Create administrator' }).click();
  await expect(page).not.toHaveURL(/\/setup/);
}

export const composer = (page: Page) => page.locator('#composer-composer');
export const threadComposer = (page: Page) => page.locator('#composer-drawer-composer');

export async function send(page: Page, text: string) {
  const box = composer(page);
  await box.fill(text);
  await box.press('Enter');
}

export function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/** Opens a channel or DM from the sidebar (names may carry an unread suffix). */
export async function openChannel(page: Page, name: string) {
  await page
    .getByRole('navigation', { name: 'Channels' })
    .getByRole('link', {
      name: new RegExp(`^(?:(?:online|offline|away) )?${escapeRe(name)}(\\s|$)`),
    })
    .click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText(name);
}

/** A delivered (not pending) message article containing text. */
export function message(page: Page, text: string) {
  return page
    .getByRole('main')
    .locator('article[aria-label^="Message from "]:not(.pending)')
    .filter({ hasText: text });
}

/** A message inside the thread drawer. */
export function threadMessage(page: Page, text: string) {
  return page
    .getByRole('complementary', { name: 'Thread' })
    .locator('article[aria-label^="Message from "]:not(.pending)')
    .filter({ hasText: text });
}

export interface AxeSummary {
  screen: string;
  scheme: string;
  /** Serious or critical violations: these fail the suite. */
  blocking: string[];
  /** Moderate or minor findings, reported but not failing. */
  minor: number;
}

/** Scans the page with axe (WCAG 2.2 AA tags) and sorts the findings. */
export async function axeScan(page: Page, screen: string, scheme: string): Promise<AxeSummary> {
  const results = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
    .analyze();
  const bad = results.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical');
  const blocking = bad.map(
    (v) =>
      `[${scheme}] ${screen}: ${v.impact} ${v.id}: ${v.help}\n` +
      v.nodes
        .slice(0, 4)
        .map((n) => `    ${n.target.join(' ')}  ${n.failureSummary?.split('\n')[1] ?? ''}`)
        .join('\n'),
  );
  return { screen, scheme, blocking, minor: results.violations.length - bad.length };
}

export interface Seed {
  admin: User;
  bea: User;
  cy: User;
}

/** First-run setup through the API plus two members, all in #general. */
export async function seed(server: Server): Promise<Seed> {
  const admin = await server.setupAdmin();
  const bea = await server.createUser('bea');
  const cy = await server.createUser('cy');
  return { admin, bea, cy };
}

export { seedContent, type Content } from './content';
