import {
  composer,
  expect,
  message,
  openChannel,
  seed,
  send,
  test,
  threadComposer,
  threadMessage,
  type Page,
  type Seed,
} from './fixtures';

test.use({ specId: 'dm-thread-reaction' });
test.describe.configure({ mode: 'serial' });

let users: Seed;
test.beforeAll(async ({ server }) => {
  users = await seed(server);
});

async function startConversation(page: Page, ...people: string[]) {
  await page.getByRole('button', { name: 'New message' }).click();
  const dialog = page.getByRole('dialog', { name: 'New message' });
  for (const p of people) await dialog.getByRole('checkbox', { name: new RegExp(p) }).check();
  await dialog.getByRole('button', { name: 'Start conversation' }).click();
  await expect(dialog).toHaveCount(0);
}

test('direct message and group direct message', async ({ open }) => {
  const a = await open(users.admin);
  const b = await open(users.bea);
  const c = await open(users.cy);
  for (const s of [a, b, c]) await s.page.goto('/');

  await startConversation(a.page, 'Bea');
  await send(a.page, 'dm hello');
  await openChannel(b.page, 'Ada Admin');
  await expect(message(b.page, 'dm hello')).toBeVisible();
  // Cy is not part of it.
  await expect(
    c.page.getByRole('navigation', { name: 'Channels' }).getByRole('link', { name: /Ada Admin/ }),
  ).toHaveCount(0);

  await startConversation(a.page, 'Bea', 'Cy');
  await send(a.page, 'group hello');
  await expect(
    c.page.getByRole('navigation', { name: 'Channels' }).getByRole('link', { name: /Bea/ }),
  ).toHaveCount(1);
  await c.page
    .getByRole('navigation', { name: 'Channels' })
    .getByRole('link', { name: /Bea/ })
    .click();
  await expect(message(c.page, 'group hello')).toBeVisible();
});

test('thread reply, reaction, pin, edit and delete', async ({ open }) => {
  const a = await open(users.admin);
  const b = await open(users.bea);
  await a.page.goto('/');
  await b.page.goto('/');
  await openChannel(a.page, 'general');
  await openChannel(b.page, 'general');

  await send(a.page, 'root message');
  const rootB = message(b.page, 'root message');
  await expect(rootB).toBeVisible();

  // Thread reply by Bea shows in her drawer and as a summary under the root.
  await rootB.hover();
  await rootB.getByRole('button', { name: 'Reply in thread' }).click();
  await expect(b.page.getByRole('heading', { name: 'Thread' })).toBeVisible();
  await threadComposer(b.page).fill('first reply');
  await threadComposer(b.page).press('Enter');
  await expect(threadMessage(b.page, 'first reply')).toBeVisible();
  const rootA = message(a.page, 'root message');
  await expect(rootA.getByRole('link', { name: /1 repl/ })).toBeVisible();
  await b.page.keyboard.press('Escape');
  await expect(b.page.getByRole('heading', { name: 'Thread' })).toHaveCount(0);

  // Reaction from Bea shows on both sides with a count.
  await rootB.hover();
  await rootB.getByRole('button', { name: 'Add reaction' }).first().click();
  const picker = b.page.getByRole('dialog', { name: 'Emoji picker' });
  await picker.getByRole('searchbox', { name: 'Search emoji' }).fill('rocket');
  await picker.getByRole('toolbar', { name: 'Emoji' }).getByRole('button').first().click();
  await expect(rootB.getByRole('button', { name: /1: toggle your reaction/ })).toBeVisible();
  await expect(rootA.getByRole('button', { name: /1: toggle your reaction/ })).toBeVisible();

  // Pin by the admin, listed in the pins panel for Bea too.
  await rootA.hover();
  await rootA.getByRole('button', { name: 'Pin message' }).click();
  await b.page.getByRole('button', { name: 'Pins' }).click();
  await expect(b.page.getByRole('heading', { name: 'Pinned messages' })).toBeVisible();
  await expect(
    b.page
      .getByRole('region', { name: 'Pinned messages' })
      .or(b.page.locator('section').filter({ hasText: 'Pinned messages' })),
  ).toContainText('root message');
  await b.page.getByRole('button', { name: 'Close' }).click();

  // Edit, then delete.
  await rootA.hover();
  await rootA.getByRole('button', { name: 'Edit' }).click();
  await a.page.getByRole('textbox', { name: 'Edit' }).fill('root message edited');
  await a.page.getByRole('button', { name: 'Save' }).click();
  await expect(message(b.page, 'root message edited')).toContainText('(edited)');

  const edited = message(a.page, 'root message edited');
  await edited.hover();
  await edited.getByRole('button', { name: 'Delete' }).click();
  await a.page
    .getByRole('alertdialog', { name: 'Delete message?' })
    .getByRole('button', { name: 'Delete' })
    .click();
  await expect(b.page.getByText('message deleted')).toBeVisible();
  await expect(composer(a.page)).toBeVisible();
});
