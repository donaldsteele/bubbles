import { composer, expect, message, seed, test, threadComposer, type Seed } from './fixtures';

test.use({ specId: 'keyboard' });
test.describe.configure({ mode: 'serial' });

let users: Seed;
test.beforeAll(async ({ server }) => {
  users = await seed(server);
  await server.createChannel(users.admin, 'random');
  const ops = await server.createChannel(users.admin, 'ops');
  await server.postMessage(users.admin, ops.id, 'ops is where the keyboard lands');
});

test('keyboard-only: channels, regions, send, thread open and close', async ({ open }) => {
  const { page } = await open(users.admin);
  await page.goto('/');
  const heading = page.getByRole('heading', { level: 1 });
  await expect(heading).toHaveText('Welcome');
  const nav = page.getByRole('navigation', { name: 'Channels' });

  // F6 from the page lands in the sidebar; Enter opens the first channel.
  await page.keyboard.press('F6');
  await expect(nav.getByRole('button', { name: 'Create channel' })).toBeFocused();
  // Safari-family engines leave links out of the Tab order by default, so the
  // first channel link is focused directly; everything after is key presses.
  await nav.getByRole('link').first().focus();
  await expect(nav.getByRole('link').first()).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(heading).toHaveText('#general');

  // Alt+Down / Alt+Up walk the channel list (sidebar order, wrapping).
  await page.keyboard.press('F6');
  await expect(nav.getByRole('link', { name: /^general/ })).toBeFocused();
  await page.keyboard.press('Alt+ArrowDown');
  await expect(heading).toHaveText('#ops');
  await page.keyboard.press('Alt+ArrowDown');
  await expect(heading).toHaveText('#random');
  await page.keyboard.press('Alt+ArrowUp');
  await expect(heading).toHaveText('#ops');

  // F6 cycles sidebar -> messages -> composer; typing and Enter sends.
  await page.keyboard.press('F6');
  await expect(nav.getByRole('link', { name: /^ops/ })).toBeFocused();
  await page.keyboard.press('F6');
  await expect(page.locator('[data-region="messages"]')).toBeFocused();
  await page.keyboard.press('F6');
  await expect(composer(page)).toBeFocused();
  await page.keyboard.type('sent without a mouse');
  await page.keyboard.press('Enter');
  await expect(message(page, 'sent without a mouse')).toBeVisible();

  // Open a thread from the keyboard, reply, and close it with Esc.
  const root = message(page, 'ops is where the keyboard lands');
  const reply = root.getByRole('button', { name: 'Reply in thread' });
  // The message is a tab stop; its actions appear while focus is inside it.
  await root.focus();
  await page.keyboard.press('Tab');
  await expect(reply).toBeFocused();
  await page.keyboard.press('Enter');
  const thread = page.getByRole('complementary', { name: 'Thread' });
  await expect(thread).toBeVisible();
  await expect(threadComposer(page)).toBeFocused();
  await page.keyboard.type('thread reply by keyboard');
  await page.keyboard.press('Enter');
  await expect(thread).toContainText('thread reply by keyboard');
  await page.keyboard.press('Escape');
  await expect(thread).toBeHidden();
  await expect(reply).toBeFocused();
});
