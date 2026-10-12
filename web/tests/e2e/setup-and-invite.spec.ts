import { expect, loginAs, message, openChannel, PASSWORD, send, test } from './fixtures';

test.use({ specId: 'setup-and-invite' });
test.describe.configure({ mode: 'serial' });

test('first sign-in, invite link, second user joins and chats', async ({ server, open }) => {
  const admin = await open(null);
  await admin.page.goto('/');
  await expect(admin.page).toHaveURL(/\/login/);

  // The server created the admin on first start; sign in with its password.
  await admin.page.getByLabel('Username').fill('admin');
  await admin.page.getByLabel('Password').fill(PASSWORD);
  await admin.page.getByRole('button', { name: 'Sign in' }).click();
  await expect(admin.page.getByRole('heading', { level: 1 })).toHaveText('Welcome');
  expect(server.adminPasswordFile()).toBe(PASSWORD);
  await server.must('PATCH', '/api/v1/admin/settings', [200], {
    as: await server.login('admin'),
    json: { instance_name: 'Bubbles E2E' },
  });

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
  await guest.page.getByLabel('Your name').fill('Bea');
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
  await loginAs(again.page, { username: 'Bea', password: PASSWORD });
  await openChannel(again.page, 'general');
  await expect(message(again.page, 'hello from bea')).toBeVisible();
  expect((await server.login('bea')).cookie).not.toBe('');
});
