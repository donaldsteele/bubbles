import { expect, loginAs, openChannel, seed, test, type Seed } from './fixtures';

test.use({ specId: 'register' });
test.describe.configure({ mode: 'serial' });

let users: Seed;

test('seed the server and open sign-up', async ({ server }) => {
  users = await seed(server);
  await server.must('PATCH', '/api/v1/admin/settings', [200], {
    as: users.admin,
    json: { open_registration: true, registration_window_hours: 0 },
  });
});

test('a new person registers from the login page and signs in by name', async ({ open }) => {
  const a = await open(null);
  await a.page.goto('/login');
  await a.page.getByRole('link', { name: 'Create an account' }).click();
  await expect(a.page).toHaveURL(/\/register$/);
  await a.page.getByLabel('Your name').fill('Ada Lovelace');
  await a.page.getByLabel('Password').fill('password1');
  await a.page.getByRole('button', { name: 'Create account' }).click();
  await openChannel(a.page, 'general');

  // A fresh browser signs in with the display name in any case.
  const b = await open(null);
  await loginAs(b.page, { username: 'ada lovelace', password: 'password1' });
  await openChannel(b.page, 'general');

  // The same name in other letters is taken.
  const c = await open(null);
  await c.page.goto('/register');
  await c.page.getByLabel('Your name').fill('ADA lovelace');
  await c.page.getByLabel('Password').fill('password1');
  await c.page.getByRole('button', { name: 'Create account' }).click();
  await expect(c.page.getByText('That name is already taken.')).toBeVisible();
});

test('the admin closes and reopens sign-up and adds a person', async ({ open }) => {
  const a = await open(users.admin);
  await a.page.goto('/admin/settings');
  await expect(a.page.getByRole('status').filter({ hasText: 'Open, no time limit' })).toBeVisible();
  await a.page.getByRole('button', { name: 'Close now' }).click();
  await expect(a.page.getByRole('status').filter({ hasText: 'Closed' })).toBeVisible();

  const guest = await open(null);
  await guest.page.goto('/login');
  await expect(guest.page.getByText('To join, ask the admin for an invite.')).toBeVisible();
  await expect(guest.page.getByRole('link', { name: 'Create an account' })).toHaveCount(0);
  await guest.page.goto('/register');
  await expect(guest.page.getByText('Sign-up is closed. To join, ask')).toBeVisible();
  await expect(guest.page.getByLabel('Your name')).toHaveCount(0);

  await a.page.getByRole('button', { name: /^Reopen/ }).click();
  await expect(a.page.getByRole('status').filter({ hasText: 'Open' }).first()).toBeVisible();
  await guest.page.goto('/login');
  await expect(guest.page.getByRole('link', { name: 'Create an account' })).toBeVisible();

  await a.page.goto('/admin/users');
  await a.page.getByLabel('Your name').fill('Bea Person');
  await a.page.getByLabel('Password').fill('pw');
  await a.page.getByRole('button', { name: 'Add person' }).click();
  await expect(a.page.getByRole('status').filter({ hasText: 'Added Bea Person.' })).toBeVisible();

  const bea = await open(null);
  await loginAs(bea.page, { username: 'Bea Person', password: 'pw' });
  await openChannel(bea.page, 'general');
});
