import { expect, openChannel, seed, test, type Seed } from './fixtures';

// Service workers need a certificate Chromium treats as valid, which only the
// Chromium fixture provides (SPKI pin). WebKit and Firefox skip this file.
test.skip(({ browserName }) => browserName !== 'chromium', 'service workers: chromium only');

test.use({ specId: 'pwa' });
test.describe.configure({ mode: 'serial' });

let users: Seed;
test.beforeAll(async ({ server }) => {
  users = await seed(server);
});

test('service worker, manifest and offline fallback', async ({ server, open }) => {
  const { page, context } = await open(users.admin);
  await page.goto('/');
  await openChannel(page, 'general');

  // The worker registers and takes control of the page.
  const scope = await page.evaluate(async () => (await navigator.serviceWorker.ready).scope);
  expect(scope).toBe(`${server.origin}/`);
  await page.reload();
  await expect
    .poll(() => page.evaluate(() => navigator.serviceWorker.controller !== null))
    .toBe(true);

  // Install criteria: a linked manifest with name, start URL, display and icons.
  const href = await page.locator('link[rel="manifest"]').getAttribute('href');
  expect(href).toBeTruthy();
  const manifest = await page.evaluate(async (u) => {
    const r = await fetch(u);
    return r.ok ? await r.json() : null;
  }, href ?? '');
  expect(manifest).not.toBeNull();
  expect(manifest.name).toBeTruthy();
  expect(manifest.start_url).toBe('/');
  expect(['standalone', 'fullscreen', 'minimal-ui']).toContain(manifest.display);
  const sizes = (manifest.icons as { sizes: string; src: string }[]).map((i) => i.sizes);
  expect(sizes).toEqual(expect.arrayContaining(['192x192', '512x512']));
  for (const icon of manifest.icons as { src: string; type: string }[]) {
    const r = await page.evaluate(async (u) => {
      const x = await fetch(u);
      return { ok: x.ok, type: x.headers.get('content-type') ?? '' };
    }, icon.src);
    expect(r.ok, icon.src).toBe(true);
    expect(r.type).toContain(icon.type);
  }

  // With the server stopped, the cached shell still opens and says it is offline.
  await server.kill();
  const offline = await context.newPage();
  await offline.goto('/offline.html');
  await expect(offline.getByRole('heading', { name: 'You are offline' })).toBeVisible();
  await offline.close();

  const shell = await page.reload();
  expect(shell?.fromServiceWorker()).toBe(true);
  await expect(page.getByRole('heading', { level: 1 }).first()).toBeVisible();

  // Back online, a reload gets the signed-in app again.
  await server.restart();
  await page.reload();
  await expect(page.getByRole('navigation', { name: 'Channels' })).toBeVisible();
});
