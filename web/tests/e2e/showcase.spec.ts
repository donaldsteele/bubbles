import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { mkdirSync, readFileSync } from 'node:fs';
import { expect, message, openChannel, test } from './fixtures';
import type { OpenOptions, Page, Server, Session, User } from './fixtures';

// Produces the marketing screenshots for the GitHub Pages showcase: a busy
// "Basement LAN" workspace with a LAN-party conversation, netdiag alerts, a DM
// with an image, search, invite, reset and admin screens, in dark and light
// themes on desktop (1280x800 at 2x) and phone (390x844 at 3x).
//
// The spec is skipped unless BUBBLES_SHOWCASE=1. It starts its own server from
// the e2e fixture, fills it through the real API and writes the PNGs to
// ../site/assets/shots (or BUBBLES_SHOWCASE_DIR). BUBBLES_SHOWCASE_IMAGE names
// the picture Bea shares in the direct message (default: fixtures/swatch.png):
//   BUBBLES_SHOWCASE=1 npm --prefix web run test:e2e -- showcase --project=chromium

const enabled = process.env.BUBBLES_SHOWCASE === '1';
const outDir = path.resolve(
  process.env.BUBBLES_SHOWCASE_DIR ?? path.join('..', 'site', 'assets', 'shots'),
);
const imagePath =
  process.env.BUBBLES_SHOWCASE_IMAGE ??
  fileURLToPath(new URL('./fixtures/swatch.png', import.meta.url));

type Scheme = 'dark' | 'light';
type Opener = (user: User | null, opts?: OpenOptions) => Promise<Session>;

const desktop = {
  viewport: { width: 1280, height: 800 },
  isMobile: false,
  hasTouch: false,
  deviceScaleFactor: 2,
};
const phone = {
  viewport: { width: 390, height: 844 },
  isMobile: true,
  hasTouch: true,
  deviceScaleFactor: 3,
};

test.use({ specId: 'showcase' });
test.describe.configure({ mode: 'serial' });

interface World {
  ada: User;
  bea: User;
  cy: User;
  dev: User;
  eli: User;
}
let w: World;

async function createChannel(
  server: Server,
  as: User,
  name: string,
  kind: 'public' | 'private',
  topic: string,
  members: User[],
) {
  const r = await server.must('POST', '/api/v1/channels', [200, 201], {
    as,
    json: { name, kind, topic },
  });
  const id = (r.json.id ?? r.json.channel?.id) as string;
  for (const m of members) {
    if (kind === 'public') await server.joinChannel(m, id);
    else
      await server.must('PUT', `/api/v1/channels/${id}/members/${m.id}`, [200, 201, 204], {
        as,
        json: {},
      });
  }
  return id;
}

async function say(server: Server, as: User, channel: string, body: string, root?: string) {
  const r = await server.must('POST', `/api/v1/channels/${channel}/messages`, [200, 201], {
    as,
    json: root ? { body, thread_root_id: root } : { body },
  });
  return (r.json.id ?? r.json.message?.id) as string;
}

async function patchMe(server: Server, as: User, display_name: string) {
  await server.must('PATCH', '/api/v1/me', [200], { as, json: { display_name } });
}

async function startDm(server: Server, as: User, others: User[]) {
  const r = await server.must('POST', '/api/v1/dms', [200, 201], {
    as,
    json: { user_ids: others.map((o) => o.id) },
  });
  return r.json.id as string;
}

test.describe('showcase screenshots', () => {
  test.skip(!enabled, 'set BUBBLES_SHOWCASE=1 to capture showcase screenshots');
  test.skip(
    ({ browserName }) => browserName !== 'chromium',
    'screenshots are captured once, in chromium',
  );

  test.beforeAll(async ({ server }) => {
    mkdirSync(outDir, { recursive: true });
    server.instance = 'Basement LAN';
    const ada = await server.setupAdmin('ada');
    await patchMe(server, ada, 'Ada');
    const bea = await server.createUser('bea');
    const cy = await server.createUser('cy');
    const dev = await server.createUser('dev');
    const eli = await server.createUser('eli');
    w = { ada, bea, cy, dev, eli };
    const crew = [bea, cy, dev, eli];

    const general = (await server.channelByName(ada, 'general')).id;
    const lan = await createChannel(
      server,
      ada,
      'lan-party',
      'public',
      'Saturday 6pm. Bring a power strip.',
      crew,
    );
    const alerts = await createChannel(
      server,
      ada,
      'netdiag-alerts',
      'public',
      'Network health from netdiag',
      [bea, cy],
    );
    const random = await createChannel(server, ada, 'random', 'public', 'Everything else', [
      bea,
      dev,
      eli,
    ]);
    const admins = await createChannel(server, ada, 'admins', 'private', 'Keys and passwords', [
      bea,
    ]);

    // #general: the printer, so search finds more than one channel.
    await say(server, bea, general, 'The living room printer dropped off the wifi again.');
    await say(server, eli, general, 'Printer is jammed too, I cleared it twice this morning.');

    // #lan-party: the main conversation.
    const up = await say(
      server,
      ada,
      lan,
      'The server is up at https://192.168.1.20:28225. Install the CA from http://192.168.1.20:28226 first, then join with your invite link.',
    );
    const switchMsg = await say(
      server,
      bea,
      lan,
      'Nice! I will bring the **16-port switch** and two long cables.',
    );
    await say(
      server,
      cy,
      lan,
      'I have the spare router for the second table. Here is the DHCP config I use:\n\n```\nsubnet 192.168.1.0 netmask 255.255.255.0 {\n  range 192.168.1.100 192.168.1.150;\n  option routers 192.168.1.1;\n  default-lease-time 43200;\n}\n```',
    );
    await say(server, dev, lan, '@ada can you reserve 192.168.1.77 for my Steam Deck?');
    await say(
      server,
      ada,
      lan,
      'Done. If anything misbehaves, netdiag will shout in #netdiag-alerts.',
    );
    const games = await say(
      server,
      eli,
      lan,
      'Game rotation: Quake III, Worms, Mario Kart, then Age of Empires II. Vote in the thread.',
    );
    const pizza = await say(
      server,
      bea,
      lan,
      'Pizza at 8. I am ordering six larges, tell me your toppings.',
    );
    await say(server, dev, lan, 'Pineapple is a valid topping. Fight me.');
    const projector = await say(
      server,
      cy,
      lan,
      'I am bringing the projector for the big-screen Mario Kart finals.',
    );
    await say(server, ada, lan, 'Perfect. See you all Saturday!');

    await say(server, bea, lan, 'Mario Kart first, the winner picks the next game.', games);
    await say(server, cy, lan, 'Worms before pizza, Age of Empires after.', games);
    await say(server, dev, lan, 'Quake III warmup at 6:30 please.', games);
    await say(server, ada, lan, 'Fine by me. I will put the schedule on the pinned note.', games);

    for (const [who, emoji] of [
      [bea, '🎉'],
      [cy, '🎉'],
      [dev, '🎉'],
      [eli, '🎉'],
      [bea, '👍'],
      [cy, '👍'],
    ] as [User, string][])
      await server.react(who, up, emoji);
    for (const who of [ada, cy, dev, eli]) await server.react(who, pizza, '🍕');
    await server.react(ada, pizza, '😂');
    await server.react(dev, pizza, '😂');
    await server.react(ada, switchMsg, '🙌');
    await server.react(eli, switchMsg, '🙌');
    await server.react(bea, projector, '🔥');
    await server.react(dev, projector, '🔥');
    await server.react(eli, games, '🎮');
    await server.react(cy, games, '🎮');
    await server.react(dev, games, '🎮');
    await server.pin(ada, lan, up);

    // #random and #admins have unread messages for Ada.
    await say(server, dev, random, 'Anyone seen my USB-C cable? Silver, slightly chewed.');
    await say(server, eli, random, 'It is under the couch with the other forty.');
    await say(server, bea, admins, 'Rotated the router admin password. It is in the vault.');
    await say(server, ada, general, 'Welcome to the Basement LAN, everyone.');
    await say(server, cy, general, 'Does the new switch support jumbo frames?');

    // Group DM and a DM with an image.
    const group = await startDm(server, cy, [ada, dev]);
    await say(server, cy, group, 'Who is driving the projector home on Saturday?');
    await say(server, dev, group, 'I can take it, I have a big trunk.');
    const dm = await startDm(server, bea, [ada]);
    await say(server, bea, dm, 'Can you take a look at the network tonight?');
    const img = await server.uploadFile(bea, 'netdiag.png', 'image/png', readFileSync(imagePath));
    await server.postWithFiles(bea, dm, 'netdiag says the printer dropped off again', [img.id]);

    // netdiag alerts in several states.
    const app = await server.createAppToken(ada, 'netdiag', ['alerts:write'], [alerts]);
    const bearer = { bearer: app.token };
    // Fields mirror what netdiag sends: the device address and the trigger.
    const post = async (severity: string, title: string, key: string, text: string) => {
      const [trigger, ip] = key.split(':');
      const fields = [
        { name: 'IP', value: ip.startsWith('192.') ? ip : '192.168.1.50', short: true },
        { name: 'Trigger', value: trigger, short: true },
      ];
      const r = await server.must('POST', '/api/v1/alerts', [200, 201], {
        as: bearer,
        json: { channel: 'netdiag-alerts', severity, title, dedupe_key: key, text, fields },
      });
      return r.json.message.id as string;
    };
    await post(
      'warning',
      'Toner low: black 15%',
      'supplyLow:black',
      'The black toner of the Brother HL-L2350 is at 15 percent.',
    );
    const resolved = await post(
      'info',
      'New device joined',
      'newDevice:192.168.1.77',
      'Steam Deck (192.168.1.77) joined the network.',
    );
    const conflict = await post(
      'warning',
      'IP conflict on 192.168.1.42',
      'ipConflict:192.168.1.42',
      'Two devices answer on 192.168.1.42.',
    );
    await post(
      'critical',
      'Printer offline',
      'offline:192.168.1.50',
      'Brother HL-L2350 at 192.168.1.50 stopped answering.',
    );
    await server.must('POST', `/api/v1/alerts/${conflict}/ack`, [200], { as: ada });
    await server.must('POST', `/api/v1/alerts/${resolved}/resolve`, [200], {
      as: ada,
      json: { text: 'Device recognised.' },
    });
  });

  /** Scrolls the message list to the newest message and parks the pointer away from it. */
  const bottom = async (page: Page) => {
    await page.evaluate(() => {
      for (const el of document.querySelectorAll('main *')) {
        if (el.scrollHeight > el.clientHeight + 20 && getComputedStyle(el).overflowY !== 'visible')
          el.scrollTop = el.scrollHeight;
      }
    });
    await page.mouse.move(2, 2);
  };

  const shoot = async (page: Page, name: string) => {
    await page.evaluate(() => document.fonts.ready);
    // The CA name carries this machine's hostname; published shots use a neutral one.
    // Hostnames are letters, digits, hyphens and dots, so only the dots need escaping.
    await page.evaluate((host) => {
      const re = new RegExp(host.replace(/\./g, '\\.'), 'gi');
      const walk = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
      for (let n = walk.nextNode(); n; n = walk.nextNode()) {
        if (n.nodeValue) n.nodeValue = n.nodeValue.replace(re, 'basement-pc');
      }
    }, os.hostname());
    await page.waitForTimeout(800);
    await page.screenshot({ path: path.join(outDir, `${name}.png`) });
  };

  /** Other people with the app open, so presence and connection counts look alive. */
  const peers = async (open: Opener) => {
    for (const u of [w.bea, w.cy, w.dev]) {
      const s = await open(u, desktop);
      await s.page.goto('/');
      await expect(s.page.getByRole('navigation', { name: 'Channels' })).toBeVisible();
    }
  };

  const main = async (
    open: Opener,
    scheme: Scheme,
    form: typeof desktop | typeof phone = desktop,
  ) => {
    const { page } = await open(w.ada, { colorScheme: scheme, ...form });
    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    return page;
  };

  const gotoChannel = async (page: Page, name: string, phoneForm = false) => {
    if (phoneForm) {
      await page.getByRole('button', { name: 'Toggle navigation' }).click();
      await page
        .getByRole('navigation', { name: 'Channels' })
        .getByRole('link', { name: new RegExp(`^${name}(\\s|$)`) })
        .click();
      await expect(page.getByRole('heading', { level: 1 })).toContainText(name);
    } else await openChannel(page, name);
  };

  const critical = (page: Page) => page.getByRole('region', { name: 'Critical: Printer offline' });

  for (const scheme of ['dark', 'light'] as Scheme[]) {
    test(`channel ${scheme}`, async ({ open }) => {
      await peers(open);
      const page = await main(open, scheme);
      await gotoChannel(page, 'lan-party');
      await expect(message(page, 'Pizza at 8')).toBeVisible();
      await expect(message(page, 'Perfect. See you')).toBeVisible();
      await bottom(page);
      await shoot(page, `channel-${scheme}`);
    });

    test(`alerts channel ${scheme}`, async ({ open }) => {
      const page = await main(open, scheme);
      await gotoChannel(page, 'netdiag-alerts');
      await expect(critical(page)).toBeVisible();
      await bottom(page);
      await shoot(page, `alerts-channel-${scheme}`);
    });
  }

  test('alerts view dark', async ({ open }) => {
    const page = await main(open, 'dark');
    await page.goto('/alerts');
    await expect(critical(page)).toBeVisible();
    await shoot(page, 'alerts-view-dark');
  });

  test('thread dark', async ({ open }) => {
    const page = await main(open, 'dark');
    await gotoChannel(page, 'lan-party');
    await message(page, 'Game rotation').getByRole('link', { name: /repl/i }).first().click();
    await expect(page.getByRole('complementary', { name: 'Thread' })).toContainText(
      'Quake III warmup',
    );
    await bottom(page);
    await shoot(page, 'thread-dark');
  });

  test('emoji dark', async ({ open }) => {
    const page = await main(open, 'dark');
    await gotoChannel(page, 'lan-party');
    await page.locator('#composer-composer').fill('Pizza is here');
    await page.getByRole('button', { name: 'Emoji picker' }).click();
    const picker = page.getByRole('dialog', { name: 'Emoji picker' });
    await expect(picker).toBeVisible();
    await expect(
      picker.getByRole('toolbar', { name: 'Emoji' }).getByRole('button').first(),
    ).toBeVisible();
    await bottom(page);
    await shoot(page, 'emoji-dark');
  });

  test('dm image light', async ({ open }) => {
    await peers(open);
    const page = await main(open, 'light');
    await gotoChannel(page, 'Bea');
    const img = page.getByRole('main').locator('img').last();
    await expect(img).toBeVisible();
    await expect
      .poll(() => img.evaluate((i: HTMLImageElement) => i.complete && i.naturalWidth))
      .toBeGreaterThan(0);
    await bottom(page);
    await shoot(page, 'dm-image-light');
  });

  test('search dark', async ({ open }) => {
    const page = await main(open, 'dark');
    await gotoChannel(page, 'general');
    await page.getByRole('button', { name: 'Search' }).click();
    const dialog = page.getByRole('dialog', { name: 'Search' });
    await dialog
      .getByRole('searchbox', { name: 'Search messages' })
      .or(dialog.getByRole('textbox', { name: 'Search messages' }))
      .fill('printer');
    await dialog.getByRole('button', { name: 'Search', exact: true }).click();
    await expect(dialog.getByRole('list').getByRole('button').nth(2)).toBeVisible();
    await shoot(page, 'search-dark');
  });

  test('invite dark', async ({ open }) => {
    const page = await main(open, 'dark');
    await page.goto('/admin/invites');
    await page.getByRole('textbox', { name: 'Note' }).fill('For Eli’s cousin');
    await page.getByRole('button', { name: 'Create invite' }).click();
    const secret = page.getByRole('region', { name: 'Invite link' });
    await expect(secret.getByRole('img', { name: 'QR code of the invite link' })).toBeVisible();
    await shoot(page, 'invite-dark');
  });

  test('reset dark', async ({ open }) => {
    const page = await main(open, 'dark');
    await page.goto('/admin/danger');
    await expect(page.getByRole('button', { name: 'Delete all content…' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Delete everything…' })).toBeVisible();
    await shoot(page, 'reset-dark');
  });

  test('login dark', async ({ open }) => {
    const { page } = await open(null, { colorScheme: 'dark', ...desktop });
    await page.goto('/login');
    await expect(page.getByLabel('Username')).toBeVisible();
    await shoot(page, 'login-dark');
  });

  test('onboarding light', async ({ server, open }) => {
    const { page } = await open(null, { colorScheme: 'light', ...desktop });
    await page.goto(`http://127.0.0.1:${server.httpPort}/`);
    await shoot(page, 'onboarding-light');
  });

  test('phone channel dark', async ({ open }) => {
    const page = await main(open, 'dark', phone);
    await gotoChannel(page, 'lan-party', true);
    await expect(message(page, 'Perfect. See you')).toBeVisible();
    await bottom(page);
    await shoot(page, 'phone-channel-dark');
  });

  test('phone alerts light', async ({ open }) => {
    const page = await main(open, 'light', phone);
    await gotoChannel(page, 'netdiag-alerts', true);
    await expect(critical(page)).toBeVisible();
    await bottom(page);
    await shoot(page, 'phone-alerts-light');
  });

  test('admin overview dark', async ({ open }) => {
    await peers(open);
    const page = await main(open, 'dark');
    await page.goto('/admin');
    await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible();
    await expect(page.getByText('Open connections', { exact: true })).toBeVisible();
    await shoot(page, 'admin-overview-dark');
  });
});
