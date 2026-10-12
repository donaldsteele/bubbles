import { runExample, waitForOutput } from './fixtures/examples';
import { composer, expect, message, openChannel, seed, test, type Seed } from './fixtures';

test.use({ specId: 'alerts-webhooks-commands' });
test.describe.configure({ mode: 'serial' });

let users: Seed;
test.beforeAll(async ({ server }) => {
  users = await seed(server);
});

test('netdiag-sender alert: card, acknowledge, resolve', async ({ server, open }) => {
  const channel = await server.createChannel(users.admin, 'netdiag-alerts');
  const app = await server.createAppToken(users.admin, 'netdiag', ['alerts:write'], [channel.id]);
  const notice = {
    trigger: 'ipConflict',
    severity: 'critical',
    ip: '192.168.1.9',
    title: 'IP address conflict',
    message: 'Two devices share 192.168.1.9',
    at: new Date().toISOString(),
    key: 'ipConflict:192.168.1.9',
  };
  const sender = runExample(
    'netdiag-sender',
    ['-conn', app.connection, '-channel', channel.name],
    JSON.stringify(notice) + '\n',
  );
  expect(await sender.done, sender.output()).toBe(0);

  const a = await open(users.admin);
  await a.page.goto('/');
  await openChannel(a.page, 'netdiag-alerts');
  const card = a.page.getByRole('region', { name: 'Critical: IP address conflict' });
  await expect(card).toBeVisible();
  await card.getByRole('button', { name: 'Acknowledge' }).click();
  await expect(card).toContainText('Acknowledged by Ada Admin');

  // It is listed under acknowledged alerts, then resolved.
  await a.page.goto('/alerts');
  await a.page.getByRole('button', { name: 'Acknowledged' }).click();
  await expect(a.page.getByRole('region', { name: 'Critical: IP address conflict' })).toBeVisible();
  await a.page
    .getByRole('region', { name: 'Critical: IP address conflict' })
    .getByRole('button', { name: 'Resolve' })
    .click();
  await expect(a.page.getByText('No open alerts.')).toBeVisible();
  await openChannel(a.page, 'netdiag-alerts');
  await expect(a.page.getByRole('region', { name: 'OK: IP address conflict' })).toContainText(
    'Resolved',
  );
});

test('Slack-format webhook renders with a webhook badge', async ({ server, open }) => {
  const general = await server.channelByName(users.admin, 'general');
  const hook = await server.createWebhook(users.admin, general.id, 'Build bot');
  const res = await server.api('POST', hook, {
    as: null,
    json: {
      text: 'Deploy *finished* for <https://example.com/build/42|build 42>',
      username: 'CI Deployer',
    },
  });
  expect(res.status, res.body.toString()).toBeLessThan(300);

  const b = await open(users.bea);
  await b.page.goto('/');
  await openChannel(b.page, 'general');
  const posted = message(b.page, 'Deploy');
  await expect(posted).toBeVisible();
  await expect(posted).toContainText('CI Deployer');
  await expect(posted.getByText(/^webhook$/i)).toBeVisible();
  await expect(posted.getByRole('link', { name: 'build 42' })).toHaveAttribute(
    'href',
    'https://example.com/build/42',
  );
  await expect(posted.locator('strong', { hasText: 'finished' })).toBeVisible();
});

test('echo-bot registers /echo and answers only the invoker', async ({ server, open }) => {
  const app = await server.createAppToken(
    users.admin,
    'echo',
    ['commands', 'events:read'],
    [(await server.channelByName(users.admin, 'general')).id],
  );
  const bot = runExample('echo-bot', ['-conn', app.connection]);
  try {
    await waitForOutput(bot, 'echo-bot ready');
    const a = await open(users.admin);
    const b = await open(users.bea);
    await a.page.goto('/');
    await b.page.goto('/');
    await openChannel(a.page, 'general');
    await openChannel(b.page, 'general');

    await composer(a.page).fill('/echo quiet words');
    await composer(a.page).press('Enter');
    const notice = a.page.getByRole('status').filter({ hasText: 'quiet words' });
    await expect(notice).toBeVisible();
    await expect(notice.getByRole('button', { name: 'Dismiss' })).toBeVisible();
    // Nobody else sees it, and it is not stored as a channel message. A marker
    // message sent after it shows that the other user's stream has caught up.
    await composer(a.page).fill('marker after the notice');
    await composer(a.page).press('Enter');
    await expect(b.page.getByText('marker after the notice')).toBeVisible();
    await expect(b.page.getByText('quiet words')).toHaveCount(0);
    await notice.getByRole('button', { name: 'Dismiss' }).click();
    await expect(notice).toHaveCount(0);
  } finally {
    await bot.stop();
  }
});
