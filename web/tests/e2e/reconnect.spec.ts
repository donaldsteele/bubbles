import { expect, message, openChannel, seed, send, test, type Seed } from './fixtures';

test.use({ specId: 'reconnect' });
test.describe.configure({ mode: 'serial' });

let users: Seed;
test.beforeAll(async ({ server }) => {
  users = await seed(server);
});

test('server restart mid-session: clients reconnect, nothing lost or duplicated', async ({
  server,
  open,
}) => {
  test.setTimeout(120_000);
  const a = await open(users.admin);
  const b = await open(users.bea);
  await a.page.goto('/');
  await b.page.goto('/');
  await openChannel(a.page, 'general');
  await openChannel(b.page, 'general');

  for (let i = 1; i <= 3; i++) await send(a.page, `before ${i}`);
  for (let i = 1; i <= 3; i++) await expect(message(b.page, `before ${i}`)).toHaveCount(1);

  // The server goes away; a message written meanwhile waits in the outbox.
  await server.kill();
  await expect(a.page.getByRole('status').filter({ hasText: /Reconnecting|offline/i })).toBeVisible(
    {
      timeout: 30_000,
    },
  );
  await send(a.page, 'written while down');
  await expect(a.page.getByText(/Sending…|Not sent/).first()).toBeVisible();

  // It comes back on the same data directory and ports.
  await server.restart();
  const general = await server.channelByName(users.admin, 'general');
  await server.postMessage(users.bea, general.id, 'after restart');

  await expect(message(b.page, 'after restart')).toHaveCount(1, { timeout: 60_000 });
  await expect(message(a.page, 'after restart')).toHaveCount(1, { timeout: 60_000 });
  await expect(message(a.page, 'written while down')).toHaveCount(1, { timeout: 60_000 });
  await expect(message(b.page, 'written while down')).toHaveCount(1, { timeout: 60_000 });
  await expect(a.page.getByText(/Sending…|Not sent/)).toHaveCount(0);

  // The stored history has every message exactly once, in both views.
  const stored = (await server.messages(users.admin, general.id)).map((m) => m.body);
  const wanted = ['before 1', 'before 2', 'before 3', 'written while down', 'after restart'];
  for (const text of wanted) {
    expect(
      stored.filter((b) => b === text),
      text,
    ).toHaveLength(1);
    await expect(message(a.page, text)).toHaveCount(1);
    await expect(message(b.page, text)).toHaveCount(1);
  }
});
