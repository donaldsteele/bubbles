import { composer, expect, message, seed, send, test, threadComposer, type Seed } from './fixtures';

test.use({ specId: 'mobile' });
test.describe.configure({ mode: 'serial' });

let users: Seed;
test.beforeAll(async ({ server }) => {
  users = await seed(server);
  const general = await server.channelByName(users.admin, 'general');
  await server.postMessage(users.bea, general.id, 'hello from the phone crowd');
});

test('375x812: navigation list, channel, thread and back', async ({ open }) => {
  const { page } = await open(users.admin, {
    viewport: { width: 375, height: 812 },
    hasTouch: true,
  });
  await page.goto('/');
  const noOverflow = () =>
    page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth);

  // The navigation list opens from the header button and closes after a choice.
  const toggle = page.getByRole('button', { name: 'Toggle navigation' });
  await expect(page.getByRole('navigation', { name: 'Channels' })).toBeHidden();
  await toggle.click();
  const list = page.getByRole('navigation', { name: 'Channels' });
  await expect(list).toBeVisible();
  await list.getByRole('link', { name: /^general/ }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('general');
  await expect(list).toBeHidden();
  expect(await noOverflow()).toBe(true);

  // A thread takes the whole width; Close brings the channel back.
  const root = message(page, 'hello from the phone crowd');
  await expect(root).toBeVisible();
  await root.getByRole('button', { name: 'Reply in thread' }).click();
  const thread = page.getByRole('complementary', { name: 'Thread' });
  await expect(thread).toBeVisible();
  const box = await thread.boundingBox();
  expect(box?.width ?? 0).toBeGreaterThanOrEqual(370);
  await threadComposer(page).fill('reply from phone');
  await threadComposer(page).press('Enter');
  await expect(thread).toContainText('reply from phone');
  expect(await noOverflow()).toBe(true);
  await thread.getByRole('button', { name: 'Close' }).click();
  await expect(thread).toBeHidden();
  await expect(composer(page)).toBeVisible();

  // The browser's back gesture steps out of a thread, too.
  await root.getByRole('link', { name: /repl/i }).first().click();
  await expect(thread).toBeVisible();
  await page.goBack();
  await expect(thread).toBeHidden();
  await send(page, 'sent from the channel view');
  await expect(message(page, 'sent from the channel view')).toBeVisible();
});
