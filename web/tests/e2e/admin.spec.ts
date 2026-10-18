import { expect, seed, test, type Seed } from './fixtures';

test.use({ specId: 'admin' });
test.describe.configure({ mode: 'serial' });

let users: Seed;
test.beforeAll(async ({ server }) => {
  users = await seed(server);
});

test('invite shows a QR code and the link only once', async ({ open }) => {
  const a = await open(users.admin);
  await a.page.goto('/admin/invites');
  await a.page.getByRole('textbox', { name: 'Note' }).fill('for the QR test');
  await a.page.getByRole('button', { name: 'Create invite' }).click();
  const secret = a.page.getByRole('region', { name: 'Invite link' });
  await expect(secret.getByRole('textbox', { name: 'Invite link' })).toHaveValue(/\/invite\//);
  await expect(secret.getByRole('img', { name: 'QR code of the invite link' })).toBeVisible();
  await expect(secret).toContainText('shown only once');
  await secret.getByRole('button', { name: 'I have saved it' }).click();
  await expect(secret).toHaveCount(0);
  // Reloading never shows the link again, but the invite is listed and can be revoked.
  await a.page.reload();
  await expect(a.page.getByRole('region', { name: 'Invite link' })).toHaveCount(0);
  const row = a.page.getByRole('row').filter({ hasText: 'for the QR test' });
  await expect(row).toBeVisible();
  await row.getByRole('button', { name: 'Revoke' }).click();
  await expect(a.page.getByRole('row').filter({ hasText: 'for the QR test' })).toContainText(
    /revoked/i,
  );
});

test('app token is created and shown once', async ({ open }) => {
  const a = await open(users.admin);
  await a.page.goto('/admin/apps');
  await a.page.getByRole('textbox', { name: 'App name' }).fill('Garden sensor');
  await a.page.getByRole('button', { name: 'Create app' }).click();
  const app = a.page.getByRole('article', { name: 'Garden sensor' });
  await expect(app).toBeVisible();
  await app.getByRole('button', { name: 'New token' }).click();
  const ask = a.page.getByRole('dialog', { name: 'New token for Garden sensor' });
  await ask.getByRole('checkbox', { name: /^alerts:write/ }).check();
  await ask.getByRole('button', { name: 'Create token' }).click();
  const shown = a.page.getByRole('region', { name: 'Token for Garden sensor' });
  const conn = shown.getByRole('textbox', { name: 'Connection string for the app' });
  await expect(conn).toHaveValue(/^bubbles:\/\/.+token=bub_/);
  await shown.getByRole('button', { name: 'I have saved it' }).click();
  await expect(shown).toHaveCount(0);
  await a.page.reload();
  await expect(a.page.getByText(/token=bub_/)).toHaveCount(0);
});

test('pairing request is approved and the app receives its token', async ({ server, open }) => {
  const created = await server.must('POST', '/api/v1/pairing/requests', [201], {
    as: null,
    json: { app_name: 'Hall display', requested_scopes: ['alerts:write'] },
  });
  const a = await open(users.admin);
  await a.page.goto('/admin/pairing');
  const req = a.page.getByRole('article', { name: 'Hall display' });
  await expect(req).toContainText(created.json.code as string);
  await req.getByRole('button', { name: 'Approve' }).click();
  // The app asks for alerts:write, so creating its alerts channel starts ticked.
  await expect(
    req.getByRole('checkbox', { name: 'Also create a channel for this app' }),
  ).toBeChecked();
  await expect(req.getByRole('note')).toHaveCount(0);
  await req.getByRole('checkbox', { name: /^alerts:write/ }).check();
  await req.getByRole('button', { name: 'Approve' }).last().click();
  await expect(
    a.page.getByRole('status').filter({ hasText: 'Hall display is connected.' }),
  ).toBeVisible();

  const polled = await server.must('GET', `/api/v1/pairing/requests/${created.json.id}`, [200], {
    as: null,
    headers: { 'X-Poll-Secret': created.json.poll_secret as string },
  });
  expect(polled.json.state).toBe('claimed');
  expect(polled.json.token).toMatch(/^bub_/);
  // The claim answer lists the channel the bot is in: the one just created.
  expect(polled.json.channels).toEqual([
    { id: expect.stringMatching(/^[0-9A-Z]{26}$/), name: 'hall-display-alerts' },
  ]);
});

test('a re-pairing reconnects the existing app and ends its old token', async ({
  server,
  open,
}) => {
  const first = await server.must('POST', '/api/v1/pairing/requests', [201], {
    as: null,
    json: { app_name: 'Lab probe', requested_scopes: ['alerts:write'] },
  });
  await server.must('POST', `/api/v1/admin/pairing-requests/${first.json.id}/approve`, [200], {
    as: users.admin,
    json: { create_channel: true },
  });
  const claim = await server.must('GET', `/api/v1/pairing/requests/${first.json.id}`, [200], {
    as: null,
    headers: { 'X-Poll-Secret': first.json.poll_secret as string },
  });
  const oldToken = { bearer: claim.json.token as string };
  const self = await server.must('GET', '/api/v1/apps/self', [200], { as: oldToken });
  const appId = (self.json.app as { id: string }).id;

  // The app pairs again, naming the app it was paired as.
  const again = await server.must('POST', '/api/v1/pairing/requests', [201], {
    as: null,
    json: { app_name: 'Lab probe', requested_scopes: ['alerts:write'], replaces_app_id: appId },
  });
  const a = await open(users.admin);
  await a.page.goto('/admin/pairing');
  const req = a.page.getByRole('article', { name: 'Lab probe' });
  await expect(req).toContainText(
    'Replaces Lab probe (@lab-probe). Its bot is in #lab-probe-alerts.',
  );
  await req.getByRole('button', { name: 'Approve' }).click();
  await expect(req.getByRole('checkbox', { name: 'Reconnect as the existing app' })).toBeChecked();
  // Its bot already has a channel: nothing new to create, no warning.
  await expect(
    req.getByRole('checkbox', { name: 'Also create a channel for this app' }),
  ).not.toBeChecked();
  await expect(req.getByRole('note')).toHaveCount(0);
  await req.getByRole('button', { name: 'Approve' }).last().click();
  await expect(
    a.page.getByRole('status').filter({ hasText: 'Lab probe is connected.' }),
  ).toBeVisible();

  // The old token works until the app claims the new one, then stops.
  await server.must('GET', '/api/v1/apps/self', [200], { as: oldToken });
  const polled = await server.must('GET', `/api/v1/pairing/requests/${again.json.id}`, [200], {
    as: null,
    headers: { 'X-Poll-Secret': again.json.poll_secret as string },
  });
  expect(polled.json.state).toBe('claimed');
  expect(polled.json.channels).toEqual([
    { id: expect.stringMatching(/^[0-9A-Z]{26}$/), name: 'lab-probe-alerts' },
  ]);
  await server.must('GET', '/api/v1/apps/self', [401], { as: oldToken });
  const now = await server.must('GET', '/api/v1/apps/self', [200], {
    as: { bearer: polled.json.token as string },
  });
  expect((now.json.app as { id: string }).id).toBe(appId);
});

test('webhook is created and its URL shown once', async ({ open }) => {
  const a = await open(users.admin);
  await a.page.goto('/admin/webhooks');
  await a.page.getByRole('combobox', { name: /^Channel/ }).selectOption({ label: '#general' });
  await a.page.getByRole('textbox', { name: 'Name' }).fill('Garage door');
  await a.page.getByRole('button', { name: 'Create webhook' }).click();
  const secret = a.page.getByRole('region', { name: 'Webhook URL' });
  await expect(secret.getByRole('textbox', { name: 'Webhook URL' })).toHaveValue(/\/hooks\//);
  await expect(secret.getByRole('img', { name: 'QR code of the webhook URL' })).toBeVisible();
  await secret.getByRole('button', { name: 'I have saved it' }).click();
  await expect(a.page.getByText('Garage door')).toBeVisible();
});

test('settings are saved and survive a reload', async ({ open }) => {
  const a = await open(users.admin);
  await a.page.goto('/admin/settings');
  const name = a.page.getByRole('textbox', { name: 'Instance name' });
  await name.fill('Renamed Bubbles');
  await a.page.getByRole('textbox', { name: 'Default retention (days)' }).fill('30');
  await a.page.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(a.page.getByRole('status').filter({ hasText: 'Saved.' })).toBeVisible();
  await a.page.reload();
  await expect(a.page.getByRole('textbox', { name: 'Instance name' })).toHaveValue(
    'Renamed Bubbles',
  );
  await expect(a.page.getByRole('textbox', { name: 'Default retention (days)' })).toHaveValue('30');
});
