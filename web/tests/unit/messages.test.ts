import { beforeEach, describe, expect, it } from 'vitest';
import type { Me, Message } from '../../src/lib/api/types';
import { applyEvent } from '../../src/lib/state/dispatch';
import { messages } from '../../src/lib/state/messages.svelte';
import { session } from '../../src/lib/state/session.svelte';
import { unread } from '../../src/lib/state/unread.svelte';

function msg(id: string, over: Partial<Message> = {}): Message {
  return {
    id,
    channel_id: 'c1',
    thread_root_id: null,
    kind: 'user',
    author: { id: 'u2', username: 'b', display_name: 'B', kind: 'human', avatar_url: null },
    override_name: null,
    body: 'hi',
    files: [],
    reactions: [],
    mentions: { user_ids: [], channel: false, here: false },
    reply_count: 0,
    last_reply_at: null,
    edited_at: null,
    deleted: false,
    created_at: '2026-01-01T00:00:00.000Z',
    alert: null,
    ...over,
  } as Message;
}

const frame = (type: string, data: unknown, channel_id = 'c1') => ({
  seq: 1,
  type,
  channel_id,
  ts: '',
  data: data as Record<string, unknown>,
});

describe('message state', () => {
  beforeEach(() => {
    messages.clear();
    unread.clear();
    session.setMe({ id: 'u1' } as Me);
  });

  it('created is idempotent and keeps ULID order', () => {
    applyEvent(frame('message.created', msg('01B')));
    applyEvent(frame('message.created', msg('01A')));
    applyEvent(frame('message.created', msg('01B')));
    expect(messages.list('c1').map((m) => m.id)).toEqual(['01A', '01B']);
  });

  it('counts unread once per message and detects mentions', () => {
    applyEvent(frame('message.created', msg('01A')));
    applyEvent(frame('message.created', msg('01A')));
    const mention = { user_ids: ['u1'], channel: false, here: false };
    applyEvent(frame('message.created', msg('01B', { mentions: mention })));
    const own = { ...msg('x').author, id: 'u1' };
    applyEvent(frame('message.created', msg('01C', { author: own })));
    expect(unread.get('c1')).toEqual({ unread: 2, mentions: 1 });
    applyEvent(
      frame('read.updated', { channel_id: 'c1', last_read_id: '01C', unread: 0, mentions: 0 }),
    );
    expect(unread.get('c1')).toEqual({ unread: 0, mentions: 0 });
  });

  it('updated replaces in place', () => {
    applyEvent(frame('message.created', msg('01A')));
    applyEvent(frame('message.updated', msg('01A', { body: 'edited' })));
    expect(messages.list('c1')).toHaveLength(1);
    expect(messages.list('c1')[0]?.body).toBe('edited');
  });

  it('deleted tombstones in place, twice is the same, and stale updates do not resurrect', () => {
    applyEvent(frame('message.created', msg('01A')));
    const del = { id: '01A', channel_id: 'c1', thread_root_id: null, deleted_at: 'x' };
    applyEvent(frame('message.deleted', del));
    applyEvent(frame('message.deleted', del));
    expect(messages.list('c1')[0]?.deleted).toBe(true);
    expect(messages.list('c1')[0]?.body).toBe('');
    applyEvent(frame('message.updated', msg('01A', { body: 'late' })));
    expect(messages.list('c1')[0]?.deleted).toBe(true);
  });

  it('delete for an unknown message is a no-op', () => {
    applyEvent(frame('message.deleted', { id: 'zzz', channel_id: 'c1' }));
    expect(messages.list('c1')).toEqual([]);
  });

  it('tolerates id-only stubs on reaction and pin events for deleted messages', () => {
    applyEvent(frame('message.created', msg('01A')));
    applyEvent(frame('reaction.added', { channel_id: 'c1', message_id: '01A', deleted: true }));
    expect(messages.list('c1')[0]?.deleted).toBe(true);
    expect(() =>
      applyEvent(frame('pin.added', { channel_id: 'c1', message_id: 'missing', deleted: true })),
    ).not.toThrow();
    expect(messages.pins.c1 ?? []).toEqual([]);
  });

  it('reactions add and remove idempotently', () => {
    applyEvent(frame('message.created', msg('01A')));
    const add = { message_id: '01A', channel_id: 'c1', user_id: 'u2', emoji: ':+1:', count: 1 };
    applyEvent(frame('reaction.added', add));
    applyEvent(frame('reaction.added', add));
    expect(messages.list('c1')[0]?.reactions).toEqual([
      { emoji: ':+1:', count: 1, user_ids: ['u2'] },
    ]);
    applyEvent(frame('reaction.removed', { ...add, count: 0 }));
    expect(messages.list('c1')[0]?.reactions).toEqual([]);
  });

  it('pins add once and remove', () => {
    const p = { channel_id: 'c1', message_id: '01A', user_id: 'u2' };
    applyEvent(frame('pin.added', p));
    applyEvent(frame('pin.added', p));
    expect(messages.pins.c1).toEqual(['01A']);
    applyEvent(frame('pin.removed', p));
    expect(messages.pins.c1).toEqual([]);
  });

  it('thread replies are kept apart from the channel list', () => {
    applyEvent(frame('message.created', msg('01B', { thread_root_id: '01A' })));
    expect(messages.list('c1')).toEqual([]);
    expect(messages.thread('01A')).toHaveLength(1);
  });
});
