import {
  axeScan,
  expect,
  openChannel,
  seed,
  seedContent,
  test,
  type AxeSummary,
  type Content,
  type Page,
  type Seed,
} from './fixtures';

// Gate: zero serious or critical axe violations (WCAG 2.2 A/AA rules) on every
// main screen in both colour schemes. Moderate and minor findings are counted
// in the summary line but do not fail the run.

test.use({ specId: 'a11y' });
test.describe.configure({ mode: 'serial' });

const schemes = ['dark', 'light'] as const;
type Scheme = (typeof schemes)[number];

let users: Seed;
let content: Content;
const summaries: AxeSummary[] = [];

async function scan(page: Page, screen: string, scheme: Scheme): Promise<void> {
  // Let route transitions and lazy chunks settle before scanning.
  await page.waitForLoadState('networkidle').catch(() => undefined);
  // Colour-scheme emulation drives the app's 'system' theme. Firefox builds that
  // ignore the emulation get the resolved theme set directly, so the scan still
  // sees the right palette.
  await page.evaluate((want) => {
    if (document.documentElement.dataset.theme !== want) {
      document.documentElement.dataset.theme = want;
    }
  }, scheme);
  summaries.push(await axeScan(page, screen, scheme));
}

test.afterAll(() => {
  const screens = new Set(summaries.map((s) => `${s.scheme}/${s.screen}`)).size;
  const blocking = summaries.reduce((n, s) => n + s.blocking.length, 0);
  const minor = summaries.reduce((n, s) => n + s.minor, 0);
  test.info().annotations.push({
    type: 'axe-summary',
    description: `${screens} scans (screen x theme), ${blocking} serious/critical, ${minor} moderate/minor`,
  });
});

test('seed the server', async ({ server }) => {
  users = await seed(server);
  content = await seedContent(server, users.admin, users.bea, users.cy);
});

for (const scheme of schemes) {
  test(`signed-out screens (${scheme})`, async ({ server, open }) => {
    const { page } = await open(null, { colorScheme: scheme });
    await page.goto('/login');
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
    await scan(page, 'login', scheme);
    await page.getByLabel('Username').fill('nobody');
    await page.getByLabel('Password').fill('wrong-password-123');
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByRole('alert')).toBeVisible();
    await scan(page, 'login (error)', scheme);

    const invite = await server.createInvite(users.admin);
    await page.goto(`/invite/${invite.code}`);
    await expect(page.getByRole('heading', { name: /^Join / })).toBeVisible();
    await scan(page, 'invite', scheme);

    await page.goto('/invite/not-a-real-code');
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    await scan(page, 'invite (invalid)', scheme);

    await page.goto('/reset/not-a-real-code');
    await expect(page.getByRole('heading', { name: 'Choose a new password' })).toBeVisible();
    await scan(page, 'password reset', scheme);

    const signup = (open: boolean) =>
      server.must('PATCH', '/api/v1/admin/settings', [200], {
        as: users.admin,
        json: { open_registration: open, registration_window_hours: 0 },
      });
    await signup(true);
    await page.goto('/register');
    await expect(page.getByRole('heading', { name: /^Join / })).toBeVisible();
    await scan(page, 'register', scheme);
    await signup(false);
    await page.goto('/register');
    await expect(page.getByRole('heading', { name: 'Sign-up is closed.' })).toBeVisible();
    await scan(page, 'register (closed)', scheme);

    await page.goto('/no/such/page');
    await expect(page.getByRole('heading', { name: 'Page not found' })).toBeVisible();
    await scan(page, 'not found', scheme);
    expect(summaries.flatMap((s) => s.blocking)).toEqual([]);
  });
}

for (const scheme of schemes) {
  test(`chat screens (${scheme})`, async ({ open }) => {
    const { page } = await open(users.admin, { colorScheme: scheme });
    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'Welcome' })).toBeVisible();
    await scan(page, 'home', scheme);

    await openChannel(page, 'general');
    await expect(page.getByText('Welcome to the')).toBeVisible();
    await scan(page, 'channel', scheme);

    // Thread drawer, pins and members share the drawer slot.
    await page.goto(`/c/${content.general}/t/${content.rootMessageId}`);
    await expect(page.getByRole('complementary', { name: 'Thread' })).toBeVisible();
    await scan(page, 'thread', scheme);

    await page.goto(`/c/${content.general}`);
    await page.getByRole('button', { name: 'Pins' }).click();
    await expect(page.getByRole('heading', { name: 'Pinned messages' })).toBeVisible();
    await scan(page, 'pins', scheme);
    await page.getByRole('button', { name: 'Members' }).click();
    await expect(page.getByRole('heading', { name: 'Members' })).toBeVisible();
    await scan(page, 'members', scheme);

    await openChannel(page, 'random');
    await expect(page.getByRole('button', { name: /^Image: / }).first()).toBeVisible();
    await scan(page, 'channel with attachments and webhook', scheme);
    await page
      .getByRole('button', { name: /^Image: / })
      .first()
      .click();
    await expect(page.getByRole('dialog', { name: 'Image viewer' })).toBeVisible();
    await scan(page, 'lightbox', scheme);
    await page.keyboard.press('Escape');

    await openChannel(page, 'netdiag-alerts');
    await expect(page.getByRole('region', { name: /^Critical: / })).toBeVisible();
    await scan(page, 'channel with alert cards', scheme);

    await page.goto('/alerts');
    await expect(page.getByRole('heading', { name: 'Alerts', level: 1 })).toBeVisible();
    await scan(page, 'alerts view', scheme);

    // Overlays and dialogs.
    await page.goto(`/c/${content.general}`);
    await page.getByRole('button', { name: 'Search' }).click();
    const search = page.getByRole('dialog', { name: 'Search' });
    await search.getByRole('searchbox', { name: 'Search messages' }).fill('house');
    await search.getByRole('button', { name: 'Search', exact: true }).click();
    await expect(search.getByRole('list').getByRole('button').first()).toBeVisible();
    await scan(page, 'search', scheme);
    await page.keyboard.press('Escape');

    await page.keyboard.press('Control+k');
    await expect(page.getByRole('dialog', { name: 'Quick switcher' })).toBeVisible();
    await scan(page, 'quick switcher', scheme);
    await page.keyboard.press('Escape');

    await page.getByRole('button', { name: 'Create channel' }).click();
    await expect(page.getByRole('dialog', { name: 'Create channel' })).toBeVisible();
    await scan(page, 'create channel', scheme);
    await page.keyboard.press('Escape');

    await page.getByRole('button', { name: 'Browse channels' }).click();
    await expect(page.getByRole('dialog', { name: 'Browse channels' })).toBeVisible();
    await scan(page, 'browse channels', scheme);
    await page.keyboard.press('Escape');

    await page.getByRole('button', { name: 'New message' }).click();
    await expect(page.getByRole('dialog', { name: 'New message' })).toBeVisible();
    await scan(page, 'new message', scheme);
    await page.keyboard.press('Escape');

    await page.getByRole('button', { name: 'Emoji picker' }).first().click();
    await expect(page.getByRole('dialog', { name: 'Emoji picker' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'grinning face', exact: true })).toBeVisible();
    await scan(page, 'emoji picker', scheme);
    await page.keyboard.press('Escape');

    const first = page.locator('article[aria-label^="Message from "]').first();
    await first.hover();
    await first.getByRole('button', { name: 'Delete' }).click();
    await expect(page.getByRole('alertdialog', { name: 'Delete message?' })).toBeVisible();
    await scan(page, 'delete message confirm', scheme);
    await page.keyboard.press('Escape');
    expect(summaries.flatMap((s) => s.blocking)).toEqual([]);
  });
}

for (const scheme of schemes) {
  test(`settings and admin screens (${scheme})`, async ({ server, open }) => {
    test.setTimeout(180_000);
    await server.must('POST', '/api/v1/pairing/requests', [201], {
      as: null,
      json: { app_name: 'Hall display', requested_scopes: ['alerts:write'] },
    });
    const { page } = await open(users.admin, { colorScheme: scheme });

    const settings: [string, string, string][] = [
      ['settings', '/settings', 'Theme'],
      ['settings: profile', '/settings/profile', 'Profile'],
      ['settings: sessions', '/settings/sessions', 'Sessions & devices'],
    ];
    for (const [name, url, heading] of settings) {
      await page.goto(url);
      await expect(page.getByRole('heading', { name: heading }).first()).toBeVisible();
      await scan(page, name, scheme);
    }

    const admin: [string, string, string][] = [
      ['admin: overview', '/admin', 'Overview'],
      ['admin: users', '/admin/users', 'Users'],
      ['admin: invites', '/admin/invites', 'Invites'],
      ['admin: channels', '/admin/channels', 'Channels'],
      ['admin: apps', '/admin/apps', 'Apps'],
      ['admin: pairing', '/admin/pairing', 'Pairing'],
      ['admin: webhooks', '/admin/webhooks', 'Webhooks'],
      ['admin: settings', '/admin/settings', 'Settings'],
      ['admin: audit log', '/admin/audit', 'Audit log'],
      ['admin: security', '/admin/security', 'Security'],
      ['admin: danger zone', '/admin/danger', 'Danger zone'],
    ];
    for (const [name, url, heading] of admin) {
      await page.goto(url);
      await expect(
        page.getByRole('main').getByRole('heading', { name: heading, level: 2 }),
      ).toBeVisible();
      // Tables and cards load after the heading: wait until the loading status is gone.
      await expect(page.getByRole('status').filter({ hasText: 'Loading' })).toHaveCount(0);
      await scan(page, name, scheme);
    }

    // Admin forms in their result states.
    await page.goto('/admin/invites');
    await page.getByRole('button', { name: 'Create invite' }).click();
    await expect(page.getByRole('region', { name: 'Invite link' })).toBeVisible();
    await scan(page, 'admin: invite link shown', scheme);

    await page.goto('/admin/pairing');
    await page
      .getByRole('article', { name: 'Hall display' })
      .first()
      .getByRole('button', { name: 'Approve' })
      .click();
    await scan(page, 'admin: pairing approval form', scheme);

    await page.goto('/admin/danger');
    for (const [trigger, title] of [
      ['Delete all content…', 'Delete all content?'],
      ['Delete everything…', 'Delete everything?'],
    ]) {
      await page.getByRole('button', { name: trigger }).click();
      await expect(page.getByRole('alertdialog', { name: title })).toBeVisible();
      await scan(page, `danger zone dialog: ${title}`, scheme);
      await page.getByRole('checkbox', { name: 'Download encrypted backup first' }).check();
      await scan(page, `danger zone dialog with backup: ${title}`, scheme);
      await page.keyboard.press('Escape');
    }
    expect(summaries.flatMap((s) => s.blocking)).toEqual([]);
  });
}
