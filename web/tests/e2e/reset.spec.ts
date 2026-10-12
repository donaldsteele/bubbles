import {
  expect,
  message,
  openChannel,
  PASSWORD,
  seed,
  send,
  setupAdminUI,
  test,
  type Page,
  type Seed,
} from './fixtures';

test.use({ specId: 'reset' });
test.describe.configure({ mode: 'serial' });

let users: Seed;
test.beforeAll(async ({ server }) => {
  users = await seed(server);
});

async function confirmReset(
  page: Page,
  trigger: string,
  title: string,
  submit: string,
  instance: string,
) {
  await page.getByRole('button', { name: trigger }).click();
  const dialog = page.getByRole('alertdialog', { name: title });
  await expect(dialog).toBeVisible();
  // The confirm button stays disabled until both fields are filled correctly.
  await expect(dialog.getByRole('button', { name: submit })).toBeDisabled();
  await dialog.getByRole('textbox', { name: 'Type the instance name to confirm' }).fill(instance);
  await dialog.getByLabel('Your password').fill(PASSWORD);
  await expect(dialog.getByRole('button', { name: submit })).toBeEnabled();
  await dialog.getByRole('button', { name: submit }).click();
  return dialog;
}

test('clearing content empties channels in the other context', async ({ server, open }) => {
  const a = await open(users.admin);
  const b = await open(users.bea);
  await a.page.goto('/');
  await b.page.goto('/');
  await openChannel(a.page, 'general');
  await openChannel(b.page, 'general');
  await send(a.page, 'soon to be erased');
  await expect(message(b.page, 'soon to be erased')).toBeVisible();

  await a.page.goto('/admin/danger');
  // A wrong confirmation text keeps the button disabled.
  await a.page.getByRole('button', { name: 'Delete all content…' }).click();
  const probe = a.page.getByRole('alertdialog', { name: 'Delete all content?' });
  await probe.getByRole('textbox', { name: 'Type the instance name to confirm' }).fill('nope');
  await probe.getByLabel('Your password').fill(PASSWORD);
  await expect(probe.getByRole('button', { name: 'Delete content' })).toBeDisabled();
  await probe.getByRole('button', { name: 'Cancel' }).click();

  await confirmReset(
    a.page,
    'Delete all content…',
    'Delete all content?',
    'Delete content',
    server.instance,
  );
  await expect(a.page.getByRole('alertdialog')).toHaveCount(0, { timeout: 30_000 });

  // The other browser lands on an empty channel and is told why.
  await expect(b.page.getByText('All messages and files were cleared by an admin.')).toBeVisible({
    timeout: 30_000,
  });
  await expect(message(b.page, 'soon to be erased')).toHaveCount(0);
  await expect(b.page.getByRole('navigation', { name: 'Channels' })).toBeVisible();
  await b.page.reload();
  await openChannel(b.page, 'general');
  await expect(message(b.page, 'soon to be erased')).toHaveCount(0);

  // Users and channels stayed: the stored history is empty but bea can post.
  await send(b.page, 'fresh start');
  await expect(message(b.page, 'fresh start')).toBeVisible();
});

test('factory reset signs everyone out and setup works again', async ({ server, open }) => {
  const a = await open(users.admin);
  const b = await open(users.bea);
  await a.page.goto('/');
  await b.page.goto('/');
  await openChannel(b.page, 'general');

  await a.page.goto('/admin/danger');
  await confirmReset(
    a.page,
    'Delete everything…',
    'Delete everything?',
    'Delete everything',
    server.instance,
  );

  // The admin sees the new setup token once, then starts setup again.
  await expect(a.page.getByRole('heading', { name: 'The server was reset' })).toBeVisible({
    timeout: 60_000,
  });
  const token = await a.page.getByRole('textbox', { name: 'Setup token' }).inputValue();
  expect(token).toMatch(/^[a-z2-7]{52}$/);
  expect(token).toBe(server.setupTokenFile());
  await a.page.getByRole('button', { name: 'Start setup' }).click();
  await expect(a.page.getByRole('heading', { name: 'Set up bubbles' })).toBeVisible();

  // The other context is signed out and told the server was reset.
  await expect(b.page.getByText('This server was reset.')).toBeVisible({ timeout: 30_000 });
  await b.page.reload();
  await expect(b.page).toHaveURL(/\/(login|setup)/);
  const old = await server.api('GET', '/api/v1/me', { as: users.bea });
  expect(old.status).toBe(401);

  // Setup with the new token creates a fresh administrator.
  await setupAdminUI(a.page, token, {
    instance: 'After Reset',
    username: 'admin2',
    password: PASSWORD,
  });
  await expect(a.page.getByRole('navigation', { name: 'Channels' })).toBeVisible();
  const status = await server.api('GET', '/api/v1/setup/status', { as: null });
  expect(JSON.stringify(status.json)).toContain('false');
});
