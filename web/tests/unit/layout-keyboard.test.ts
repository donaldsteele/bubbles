import { describe, expect, it } from 'vitest';
import { RateGate, summarizeNew, typingPhrase } from '../../src/lib/a11y/announce';
import { channelLabel, groupChannels, orderedIds } from '../../src/lib/channels';
import type { Channel, User } from '../../src/lib/api/types';
import { adjacentChannel, cycle, nextUnreadChannel } from '../../src/lib/keyboard';
import { buildRows } from '../../src/lib/messages/layout';
import { senderName } from '../../src/lib/message-actions';
import { msg } from './factory';

describe('buildRows', () => {
  it('groups same-author messages within 5 minutes only', () => {
    const rows = buildRows([
      msg(1),
      msg(2),
      msg(400), // 6m39s after msg 2
      msg(401, { author: { ...msg(1).author, id: 'U2' } }),
    ]);
    const flags = rows.filter((r) => r.type === 'msg').map((r) => r.type === 'msg' && r.grouped);
    expect(flags).toEqual([false, true, false, false]);
  });

  it('inserts day dividers and breaks grouping across days', () => {
    const next = msg(2, { created_at: '2026-01-02T12:00:01.000Z' });
    const rows = buildRows([msg(1), next]);
    expect(rows.map((r) => r.type)).toEqual(['day', 'msg', 'day', 'msg']);
    expect(rows[3]?.type === 'msg' && rows[3].grouped).toBe(false);
  });

  it('places the New divider after last_read_id, skipping own messages', () => {
    const other = { ...msg(1).author, id: 'U2' };
    const items = [msg(1), msg(2, { author: other }), msg(3, { author: other })];
    const rows = buildRows(items, { lastReadId: 'M00001', meId: 'U9' });
    expect(rows.map((r) => r.key)).toEqual([
      expect.stringContaining('day:'),
      'M00001',
      'new',
      'M00002',
      'M00003',
    ]);
    const own = buildRows(items, { lastReadId: 'M00001', meId: 'U1' });
    expect(own.some((r) => r.type === 'new')).toBe(true); // M00002 is from U2
    const none = buildRows(items, { lastReadId: null });
    expect(none.some((r) => r.type === 'new')).toBe(false);
  });

  it('never groups tombstones or alerts', () => {
    const rows = buildRows([msg(1), msg(2, { deleted: true }), msg(3, { kind: 'alert' })]);
    expect(rows.filter((r) => r.type === 'msg' && r.grouped)).toHaveLength(0);
  });
});

describe('webhook sender', () => {
  it('uses override_name only for app authors', () => {
    const app = msg(1, {
      override_name: 'Deploy',
      author: { id: 'A1', username: 'bot', display_name: 'Bot', kind: 'app', avatar_url: null },
    });
    expect(senderName(app)).toEqual({ name: 'Deploy', webhook: true });
    expect(senderName(msg(2, { override_name: 'Ana2' }))).toEqual({ name: 'Ana', webhook: false });
  });
});

describe('keyboard helpers', () => {
  const ids = ['a', 'b', 'c', 'd'];

  it('cycles with wrap', () => {
    expect(adjacentChannel(ids, 'b', 1)).toBe('c');
    expect(adjacentChannel(ids, 'd', 1)).toBe('a');
    expect(adjacentChannel(ids, 'a', -1)).toBe('d');
    expect(adjacentChannel(ids, undefined, 1)).toBe('a');
    expect(adjacentChannel(ids, undefined, -1)).toBe('d');
    expect(adjacentChannel([], 'a', 1)).toBeUndefined();
    expect(cycle(['sidebar', 'messages', 'composer', 'drawer'], 'drawer', 1)).toBe('sidebar');
  });

  it('finds the next unread channel in either direction', () => {
    const unread = (id: string) => id === 'a' || id === 'c';
    expect(nextUnreadChannel(ids, 'a', 1, unread)).toBe('c');
    expect(nextUnreadChannel(ids, 'c', 1, unread)).toBe('a');
    expect(nextUnreadChannel(ids, 'c', -1, unread)).toBe('a');
    expect(nextUnreadChannel(ids, 'b', -1, unread)).toBe('a');
    expect(nextUnreadChannel(ids, 'b', 1, () => false)).toBeUndefined();
    expect(nextUnreadChannel(ids, undefined, 1, unread)).toBe('a');
  });
});

describe('sidebar grouping', () => {
  const user = (id: string, name: string): User =>
    ({ id, username: name.toLowerCase(), display_name: name }) as User;
  const users = { U1: user('U1', 'Me'), U2: user('U2', 'Ben'), U3: user('U3', 'Ana') };
  const ch = (id: string, kind: Channel['kind'], extra: Partial<Channel> = {}): Channel =>
    ({ id, kind, name: id, archived_at: null, participant_ids: [], ...extra }) as Channel;

  it('splits channels and DMs, hides archived, labels DMs and note-to-self', () => {
    const joined = [
      ch('zeta', 'public'),
      ch('alpha', 'private'),
      ch('old', 'public', { archived_at: '2026-01-01T00:00:00.000Z' }),
      ch('d1', 'dm', { name: undefined, participant_ids: ['U1', 'U2'] }),
      ch('d2', 'group_dm', { name: undefined, participant_ids: ['U1', 'U2', 'U3'] }),
      ch('d3', 'dm', { name: undefined, participant_ids: ['U1'] }),
    ];
    const g = groupChannels(joined, users, 'U1');
    expect(g.channels.map((c) => c.id)).toEqual(['alpha', 'zeta']);
    expect(g.dms.map((c) => channelLabel(c, users, 'U1'))).toEqual(['Ana, Ben', 'Ben', 'Me (you)']);
    expect(orderedIds(g)).toHaveLength(5);
  });
});

describe('announcements', () => {
  it('summarises new messages', () => {
    const name = (m: { author: { display_name: string } }) => m.author.display_name;
    expect(summarizeNew([], name)).toBe('');
    expect(summarizeNew([msg(1)], name)).toBe('New message from Ana');
    expect(summarizeNew([msg(1), msg(2), msg(3)], name)).toBe('3 new messages from Ana');
    const other = msg(4, { author: { ...msg(1).author, display_name: 'Ben' } });
    expect(summarizeNew([msg(1), other], name)).toBe('2 new messages from several people');
  });

  it('phrases typing users', () => {
    expect(typingPhrase([])).toBe('');
    expect(typingPhrase(['Ana'])).toBe('Ana is typing…');
    expect(typingPhrase(['Ana', 'Ben'])).toBe('Ana and Ben are typing…');
    expect(typingPhrase(['a', 'b', 'c'])).toBe('Several people are typing…');
  });

  it('lets an announcement through at most once per gap', () => {
    const g = new RateGate(10_000);
    expect(g.tryPass(1000)).toBe(true);
    expect(g.tryPass(5000)).toBe(false);
    expect(g.tryPass(11_000)).toBe(true);
  });
});
