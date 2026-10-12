import { composer, expect, message, openChannel, seed, send, test, type Seed } from './fixtures';

test.use({ specId: 'realtime-chat' });
test.describe.configure({ mode: 'serial' });

let users: Seed;
test.beforeAll(async ({ server }) => {
  users = await seed(server);
});

test('two contexts see each other live and the typing indicator shows', async ({ open }) => {
  const a = await open(users.admin);
  const b = await open(users.bea);
  await a.page.goto('/');
  await b.page.goto('/');
  await openChannel(a.page, 'general');
  await openChannel(b.page, 'general');

  await send(a.page, 'ping from admin');
  await expect(message(b.page, 'ping from admin')).toBeVisible();
  await expect(message(a.page, 'ping from admin')).toBeVisible();

  await send(b.page, 'pong from bea');
  await expect(message(a.page, 'pong from bea')).toBeVisible();

  // Typing is relayed while a draft is being written and stops after sending.
  await composer(b.page).pressSequentially('still typing', { delay: 30 });
  await expect(a.page.getByRole('status').filter({ hasText: 'Bea is typing' })).toHaveCount(1);
  await composer(b.page).fill('');

  // Several messages arrive in order and exactly once.
  for (let i = 1; i <= 5; i++) await send(a.page, `burst ${i}`);
  for (let i = 1; i <= 5; i++) await expect(message(b.page, `burst ${i}`)).toHaveCount(1);
  const texts = await b.page
    .getByRole('article', { name: /^Message from / })
    .filter({ hasText: 'burst' })
    .allInnerTexts();
  expect(texts.map((t) => /burst \d/.exec(t)?.[0])).toEqual([
    'burst 1',
    'burst 2',
    'burst 3',
    'burst 4',
    'burst 5',
  ]);
});
