import type { Message } from '../../src/lib/api/types';

export function msg(n: number, over: Partial<Message> = {}): Message {
  const id = `M${String(n).padStart(5, '0')}`;
  return {
    id,
    channel_id: 'C1',
    thread_root_id: null,
    kind: 'text',
    author: { id: 'U1', username: 'ana', display_name: 'Ana', kind: 'human', avatar_url: null },
    override_name: null,
    body: `m${n}`,
    files: [],
    reactions: [],
    mentions: { user_ids: [], channel: false, here: false },
    reply_count: 0,
    last_reply_at: null,
    edited_at: null,
    deleted: false,
    created_at: new Date(Date.UTC(2026, 0, 1, 12, 0, n)).toISOString(),
    alert: null,
    ...over,
  };
}
