import { expect, openChannel, seed, test, type Seed } from './fixtures';

test.use({ specId: 'search' });
test.describe.configure({ mode: 'serial' });

let users: Seed;
test.beforeAll(async ({ server }) => {
  users = await seed(server);
  const general = await server.channelByName(users.admin, 'general');
  const ops = await server.createChannel(users.admin, 'ops');
  await server.joinChannel(users.bea, ops.id);
  await server.postMessage(users.admin, general.id, 'quarterly zebra report is ready');
  await server.postMessage(users.bea, general.id, 'zebra migration plan https://example.com/plan');
  await server.postMessage(users.bea, ops.id, 'zebra pager rota for next week');
  await server.postMessage(users.admin, ops.id, 'unrelated text');
});

test('search filters and snippet highlighting', async ({ open }) => {
  const a = await open(users.admin);
  await a.page.goto('/');
  await openChannel(a.page, 'general');
  await a.page.getByRole('button', { name: 'Search' }).click();
  const dialog = a.page.getByRole('dialog', { name: 'Search' });
  const hits = dialog.getByRole('list').getByRole('button');

  await dialog
    .getByRole('searchbox', { name: 'Search messages' })
    .or(dialog.getByRole('textbox', { name: 'Search messages' }))
    .fill('zebra');
  await dialog.getByRole('button', { name: 'Search', exact: true }).click();
  await expect(hits).toHaveCount(3);
  // The matched word is highlighted in the snippet.
  await expect(dialog.locator('mark').first()).toHaveText(/zebra/i);

  // From: only Bea's messages.
  await dialog.getByLabel('From').selectOption({ label: '@bea' });
  await dialog.getByRole('button', { name: 'Search', exact: true }).click();
  await expect(hits).toHaveCount(2);

  // In channel: ops only.
  await dialog.getByLabel('In channel').selectOption({ label: '#ops' });
  await dialog.getByRole('button', { name: 'Search', exact: true }).click();
  await expect(hits).toHaveCount(1);
  await expect(hits.first()).toContainText('pager rota');

  // Has link, any channel and author.
  await dialog.getByLabel('In channel').selectOption({ index: 0 });
  await dialog.getByLabel('Has').selectOption({ label: 'Link' });
  await dialog.getByRole('button', { name: 'Search', exact: true }).click();
  await expect(hits).toHaveCount(1);
  await expect(hits.first()).toContainText('migration plan');

  // A date window in the past finds nothing; opening a hit jumps to the channel.
  await dialog.getByLabel('Has').selectOption({ index: 0 });
  await dialog.getByLabel('From').selectOption({ index: 0 });
  await dialog.getByLabel('Before').fill('2001-01-01');
  await dialog.getByRole('button', { name: 'Search', exact: true }).click();
  await expect(dialog.getByText('No results.')).toBeVisible();

  await dialog.getByLabel('Before').fill('');
  await dialog.getByRole('button', { name: 'Search', exact: true }).click();
  await expect(hits).toHaveCount(3);
  await hits.filter({ hasText: 'pager rota' }).click();
  await expect(a.page.getByRole('dialog', { name: 'Search' })).toHaveCount(0);
  await expect(a.page.getByRole('heading', { level: 1 })).toContainText('ops');
});
