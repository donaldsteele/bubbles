import {
  buildJpegWithGps,
  composer,
  expect,
  hasGpsMetadata,
  message,
  openChannel,
  seed,
  test,
  type Seed,
} from './fixtures';

test.use({ specId: 'uploads' });
test.describe.configure({ mode: 'serial' });

let users: Seed;
test.beforeAll(async ({ server }) => {
  users = await seed(server);
});

const PNG_1X1 = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg==',
  'base64',
);

test('GPS metadata is stripped on upload and the lightbox works from the keyboard', async ({
  server,
  open,
}) => {
  expect(hasGpsMetadata(buildJpegWithGps())).toBe(true);

  const a = await open(users.admin);
  const b = await open(users.bea);
  await a.page.goto('/');
  await b.page.goto('/');
  await openChannel(a.page, 'general');
  await openChannel(b.page, 'general');

  await a.page
    .locator('input[type="file"]')
    .first()
    .setInputFiles([
      { name: 'trip.jpg', mimeType: 'image/jpeg', buffer: buildJpegWithGps() },
      { name: 'dot.png', mimeType: 'image/png', buffer: PNG_1X1 },
    ]);
  const queue = a.page.getByRole('list', { name: 'Uploads' });
  await expect(queue).toContainText('trip.jpg');
  await expect(queue.getByText('Uploaded').first()).toBeVisible();
  await composer(a.page).fill('holiday photos');
  await composer(a.page).press('Enter');

  const mine = message(a.page, 'holiday photos');
  const theirs = message(b.page, 'holiday photos');
  await expect(mine.getByRole('button', { name: 'Image: trip.jpg' })).toBeVisible();
  await expect(theirs.getByRole('button', { name: 'Image: dot.png' })).toBeVisible();

  // The server stored the image without GPS: flag set, bytes clean.
  const general = await server.channelByName(users.admin, 'general');
  const page = await server.must('GET', `/api/v1/channels/${general.id}/messages?limit=50`, [200], {
    as: users.admin,
  });
  const posted = (
    page.json.items as { files: { name: string; meta_stripped: boolean; url: string }[] }[]
  )
    .flatMap((m) => m.files ?? [])
    .find((f) => f.name === 'trip.jpg');
  expect(posted?.meta_stripped).toBe(true);
  const dl = await server.must('GET', posted?.url ?? '', [200], { as: users.admin });
  expect(dl.body.subarray(0, 2).toString('hex')).toBe('ffd8');
  expect(hasGpsMetadata(dl.body)).toBe(false);
  expect(dl.body.includes(Buffer.from('http://ns.adobe.com'))).toBe(false);

  // Lightbox: open with the keyboard, move with the arrows, close with Esc.
  const opener = theirs.getByRole('button', { name: 'Image: trip.jpg' });
  await opener.focus();
  await b.page.keyboard.press('Enter');
  const box = b.page.getByRole('dialog', { name: 'Image viewer' });
  await expect(box).toBeVisible();
  await expect(box.getByText('1 of 2')).toBeVisible();
  await b.page.keyboard.press('ArrowRight');
  await expect(box.getByText('2 of 2')).toBeVisible();
  await expect(box.getByRole('img', { name: 'Image: dot.png' })).toBeVisible();
  await b.page.keyboard.press('ArrowLeft');
  await expect(box.getByText('1 of 2')).toBeVisible();
  await b.page.keyboard.press('Escape');
  await expect(box).toHaveCount(0);
  await expect(opener).toBeFocused();
});
