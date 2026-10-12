import { describe, expect, it } from 'vitest';
import {
  EMOJI_CACHE,
  classify,
  clickTarget,
  isNavigateMessage,
  notificationFor,
  parsePush,
} from '../../src/lib/pwa/sw-core';
import { nextUpdateState, type UpdateState } from '../../src/lib/pwa/update';

const ORIGIN = 'https://bubbles.local:28225';
const u = (p: string) => new URL(p, ORIGIN);

describe('classify', () => {
  const cases: [string, string, string, string][] = [
    ['/api/v1/channels', 'GET', 'cors', 'network-only'],
    ['/api/v1/messages', 'POST', 'cors', 'network-only'],
    ['/api/v1/ws', 'GET', 'websocket', 'network-only'],
    ['/hooks/abc', 'POST', 'cors', 'network-only'],
    ['/auth/login', 'GET', 'cors', 'network-only'],
    ['/setup', 'GET', 'navigate', 'network-only'],
    ['/setup/status', 'GET', 'cors', 'network-only'],
    ['/c/abc', 'GET', 'navigate', 'navigation'],
    ['/', 'GET', 'navigate', 'navigation'],
    ['/emoji/data.json', 'GET', 'cors', 'emoji-cache'],
    ['/assets/app-1.js', 'GET', 'no-cors', 'default'],
    ['/apiary', 'GET', 'navigate', 'navigation'],
  ];
  it.each(cases)('%s %s %s -> %s', (path, method, mode, want) => {
    expect(classify(u(path), ORIGIN, method, mode)).toBe(want);
  });

  it('never caches other origins or websocket URLs', () => {
    expect(classify(new URL('https://example.com/x.js'), ORIGIN, 'GET', 'no-cors')).toBe(
      'network-only',
    );
    expect(classify(new URL('wss://bubbles.local:28225/api/v1/ws'), ORIGIN, 'GET', 'cors')).toBe(
      'network-only',
    );
  });

  it('names the emoji cache with the bubbles- prefix', () => {
    expect(EMOJI_CACHE.startsWith('bubbles-')).toBe(true);
  });
});

describe('push payload', () => {
  it('maps a minimal payload without a body', () => {
    const plan = notificationFor(
      parsePush(JSON.stringify({ title: 'Ana in #general', channel_id: 'c1', message_id: 'm1' })),
    );
    expect(plan.title).toBe('Ana in #general');
    expect(plan.options.body).toBeUndefined();
    expect(plan.options.tag).toBe('c1');
    expect(plan.options.data).toEqual({ channel_id: 'c1', message_id: 'm1' });
    expect(plan.options.icon).toBe('/icons/icon-192.png');
  });

  it('includes the body of a full payload', () => {
    const plan = notificationFor(
      parsePush(JSON.stringify({ title: 't', channel_id: 'c', message_id: 'm', body: 'hello' })),
    );
    expect(plan.options.body).toBe('hello');
  });

  it.each([null, '', 'not json', '42', '{"title":3}'])('falls back for %j', (raw) => {
    const plan = notificationFor(parsePush(raw));
    expect(plan.title).toBe('bubbles');
    expect(plan.options.tag).toBeUndefined();
  });
});

describe('clickTarget', () => {
  it('opens the channel around the message', () => {
    expect(clickTarget({ channel_id: 'c1', message_id: 'm 1' })).toBe('/c/c1?around=m%201');
  });
  it('opens the channel without a message', () => {
    expect(clickTarget({ channel_id: 'c1' })).toBe('/c/c1');
  });
  it('falls back to the root', () => {
    expect(clickTarget(null)).toBe('/');
    expect(clickTarget({ channel_id: 7 })).toBe('/');
  });
  it('recognises the navigate message', () => {
    expect(isNavigateMessage({ type: 'navigate', path: '/c/x' })).toBe(true);
    expect(isNavigateMessage({ type: 'navigate' })).toBe(false);
    expect(isNavigateMessage(null)).toBe(false);
  });
});

describe('update prompt state machine', () => {
  const run = (events: Parameters<typeof nextUpdateState>[1][]): UpdateState =>
    events.reduce<UpdateState>((s, e) => nextUpdateState(s, e), 'idle');

  it('prompts when a worker is waiting', () => {
    expect(run(['waiting'])).toBe('available');
  });
  it('does not reload on the first install claim', () => {
    expect(run(['controllerchange'])).toBe('idle');
    expect(run(['waiting', 'controllerchange'])).toBe('available');
  });
  it('reloads only after apply and controllerchange', () => {
    expect(run(['waiting', 'apply'])).toBe('applying');
    expect(run(['waiting', 'apply', 'controllerchange'])).toBe('reloading');
  });
  it('ignores apply without an update and repeated events', () => {
    expect(run(['apply'])).toBe('idle');
    expect(run(['waiting', 'waiting', 'apply', 'apply'])).toBe('applying');
  });
});
