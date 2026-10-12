import {
  expect,
  loginAs,
  message,
  openChannel,
  PASSWORD,
  send,
  SETUP_TOKEN,
  setupAdminUI,
  test,
} from './fixtures';

test.use({ specId: 'setup-and-invite' });
test.describe.configure({ mode: 'serial' });

test('setup wizard, invite link, second user joins and chats', async ({ server, open }) => {
  const admin = await open(null);
  await admin.page.goto('/');
  await expect(admin.page).toHaveURL(/\/setup/);

  // A wrong token is refused, the right one creates the administrator.
  await admin.page.getByLabel('Setup token').fill('0'.repeat(52));
  await admin.page.getByLabel('Instance name').fill('Bubbles E2E');
  await admin.page.getByLabel('Username').fill('admin');
  await admin.page.getByLabel('Password').fill(PASSWORD);
  await admin.page.getByRole('button', { name: 'Create administrator' }).click();
  await expect(admin.page.getByRole('alert')).toContainText('setup token was not accepted');
  await setupAdminUI(admin.page, SETUP_TOKEN, {
    instance: 'Bubbles E2E',
    username: 'admin',
    password: PASSWORD,
  });
  await expect(admin.page.getByRole('heading', { level: 1 })).toHaveText('Welcome');

  // Create an invite in the admin UI; the link is shown once.
  await admin.page.goto('/admin/invites');
  await admin.page.getByRole('button', { name: 'Create invite' }).click();
  const link = admin.page.getByRole('textbox', { name: 'Invite link' });
  await expect(link).toBeVisible();
  const url = await link.inputValue();
  const code = url.split('/invite/')[1];
  expect(code).toBeTruthy();
  await admin.page.getByRole('button', { name: 'I have saved it' }).click();
  await expect(link).toHaveCount(0);

  // A second browser context accepts the invite.
  const guest = await open(null);
  await guest.page.goto(`/invite/${code}`);
  await expect(guest.page.getByRole('heading', { level: 1 })).toContainText('Join Bubbles E2E');
  await guest.page.getByLabel('Username').fill('bea');
  await guest.page.getByLabel('Display name').fill('Bea');
  await guest.page.getByLabel('Password').fill(PASSWORD);
  await guest.page.getByRole('button', { name: 'Create account' }).click();
  await openChannel(guest.page, 'general');

  // The used link is no longer usable beyond its limit; the new user chats.
  await send(guest.page, 'hello from bea');
  await expect(message(guest.page, 'hello from bea')).toBeVisible();
  await openChannel(admin.page, 'general');
  await expect(message(admin.page, 'hello from bea')).toBeVisible();

  // Signing out and in again through the login form works.
  const again = await open(null);
  await loginAs(again.page, { username: 'bea', password: PASSWORD });
  await openChannel(again.page, 'general');
  await expect(message(again.page, 'hello from bea')).toBeVisible();
  expect((await server.login('bea')).cookie).not.toBe('');
});
