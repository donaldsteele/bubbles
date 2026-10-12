import { expect, openChannel, seed, test, type Page, type Server, type User } from './fixtures';

test.use({ specId: 'unread' });

const reply = (server: Server, as: User, channelId: string, rootId: string, body: string) =>
  server.must('POST', `/api/v1/channels/${channelId}/messages`, [200, 201], {
    as,
    json: { body, thread_root_id: rootId },
  });

const badge = (page: Page, name: string) =>
  page
    .getByRole('navigation', { name: 'Channels' })
    .getByRole('link', { name: new RegExp(`^${name}(\\s|$)`) });

test('thread replies after the newest message clear when the channel is read', async ({
  open,
  server,
}) => {
  const u = await seed(server);
  const lan = await server.createChannel(u.admin, 'lan-party');
  await server.joinChannel(u.bea, lan.id);
  const root = await server.postMessage(u.bea, lan.id, 'vote in the thread');
  await server.postMessage(u.admin, lan.id, 'my newest message');
  for (const n of [1, 2, 3]) await reply(server, u.bea, lan.id, root.id as string, `reply ${n}`);

  const { page } = await open(u.admin);
  await page.goto('/');
  await openChannel(page, 'general');
  await expect(badge(page, 'lan-party')).toContainText('4');

  // Opening the channel reads the replies, too.
  await openChannel(page, 'lan-party');
  await expect(badge(page, 'lan-party')).not.toContainText('unread');

  // A reply elsewhere raises the badge; one in the open channel is read at once.
  await openChannel(page, 'general');
  await reply(server, u.bea, lan.id, root.id as string, 'reply while away');
  await expect(badge(page, 'lan-party')).toContainText('1');
  await openChannel(page, 'lan-party');
  await expect(badge(page, 'lan-party')).not.toContainText('unread');
  await reply(server, u.bea, lan.id, root.id as string, 'reply while reading');
  await expect(page.getByRole('main')).toContainText('5 replies');
  await expect(badge(page, 'lan-party')).not.toContainText('unread');

  // The server agrees after a reload.
  await page.reload();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('lan-party');
  await expect(badge(page, 'lan-party')).not.toContainText('unread');
});
