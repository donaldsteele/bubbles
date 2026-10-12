import { buildJpegWithGps, buildPng } from './jpeg';
import type { Server, User } from './server';

export interface Content {
  general: string;
  random: string;
  alerts: string;
  rootMessageId: string;
}

/**
 * Fills a freshly set-up server with a realistic mix: Markdown, a code block,
 * a reaction, a pin, a thread, image and file attachments, an alert and a
 * webhook message. Used by the accessibility scans and the screenshots.
 */
export async function seedContent(
  server: Server,
  admin: User,
  bea: User,
  cy: User,
): Promise<Content> {
  const general = await server.channelByName(admin, 'general');
  const random = await server.createChannel(admin, 'random');
  const alerts = await server.createChannel(admin, 'netdiag-alerts');
  await server.joinChannel(bea, random.id);
  await server.joinChannel(cy, random.id);

  const welcome = await server.postMessage(
    bea,
    general.id,
    'Welcome to the **house chat**. Notes live in #random, the printer is `192.168.1.50`.',
  );
  await server.postMessage(
    cy,
    general.id,
    'Config for the router:\n\n```\nlease_time 12h\ndns 192.168.1.1\n```',
  );
  await server.react(cy, welcome.id as string, '👍');
  await server.react(admin, welcome.id as string, '👍');
  await server.pin(admin, general.id, welcome.id as string);
  await server.postMessage(admin, general.id, 'Thanks Bea, pinned for everyone.');
  const reply = await server.must('POST', `/api/v1/channels/${general.id}/messages`, [200, 201], {
    as: cy,
    json: { body: 'Thread replies stay out of the main flow.', thread_root_id: welcome.id },
  });
  void reply;

  const photo = await server.uploadFile(bea, 'garden.jpg', 'image/jpeg', buildJpegWithGps());
  const dot = await server.uploadFile(bea, 'swatch.png', 'image/png', buildPng());
  const doc = await server.uploadFile(
    cy,
    'router-notes.txt',
    'text/plain',
    Buffer.from('Router notes\n'),
  );
  await server.postWithFiles(bea, random.id, 'Photos from the weekend', [photo.id, dot.id]);
  await server.postWithFiles(cy, random.id, 'Notes attached', [doc.id]);

  const app = await server.createAppToken(admin, 'netdiag', ['alerts:write'], [alerts.id]);
  const bearer = { bearer: app.token };
  await server.must('POST', '/api/v1/alerts', [200, 201], {
    as: bearer,
    json: {
      channel: alerts.name,
      severity: 'critical',
      title: 'IP address conflict',
      dedupe_key: 'ipConflict:192.168.1.9',
      text: 'Two devices share 192.168.1.9',
      fields: [
        { name: 'Address', value: '192.168.1.9', short: true },
        { name: 'Devices', value: '2', short: true },
      ],
    },
  });
  await server.must('POST', '/api/v1/alerts', [200, 201], {
    as: bearer,
    json: {
      channel: alerts.name,
      severity: 'warning',
      title: 'Toner low',
      dedupe_key: 'supplyLow:black',
      text: 'The black toner is at 8 percent.',
    },
  });
  const hook = await server.createWebhook(admin, random.id, 'Build bot');
  await server.api('POST', hook, {
    as: null,
    json: { text: 'Backup *finished* in 4 minutes', username: 'Nightly backup' },
  });

  return { general: general.id, random: random.id, alerts: alerts.id, rootMessageId: welcome.id };
}
