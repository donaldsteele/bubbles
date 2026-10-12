import { api, call } from './api/client';
import type { Message, Me } from './api/types';
import type { OutboxItem } from './composer/outbox.svelte';
import { messages } from './state/messages.svelte';

export async function editMessage(id: string, body: string): Promise<void> {
  const m = await call(api.PATCH('/messages/{id}', { params: { path: { id } }, body: { body } }));
  messages.upsert(m);
}

export async function deleteMessage(m: Pick<Message, 'id' | 'channel_id'>): Promise<void> {
  await call(api.DELETE('/messages/{id}', { params: { path: { id: m.id } } }));
  messages.tombstone(m.channel_id, m.id);
}

/** Display name for a message: webhook override for apps, else the author. */
export function senderName(m: Message): { name: string; webhook: boolean } {
  if (m.author.kind === 'app' && m.override_name) return { name: m.override_name, webhook: true };
  return { name: m.author.display_name || m.author.username, webhook: false };
}

/** Synthetic message for an optimistic (not yet stored) send. */
export function pendingMessage(item: OutboxItem, me: Me | null): Message {
  return {
    id: `pending:${item.nonce}`,
    channel_id: item.channelId,
    thread_root_id: item.threadRootId,
    kind: 'text',
    author: {
      id: me?.id ?? '',
      username: me?.username ?? '',
      display_name: me?.display_name ?? '',
      kind: 'human',
      avatar_url: null,
    },
    override_name: null,
    body: item.body,
    files: [],
    reactions: [],
    mentions: { user_ids: [], channel: false, here: false },
    reply_count: 0,
    last_reply_at: null,
    edited_at: null,
    deleted: false,
    created_at: item.createdAt,
    alert: null,
  };
}

/** Adds or removes the signed-in user's reaction and applies the returned list. */
export async function toggleReaction(m: Message, emoji: string, meId: string): Promise<void> {
  const mine = m.reactions.find((r) => r.emoji === emoji)?.user_ids.includes(meId) ?? false;
  const params = { params: { path: { id: m.id, emoji } } };
  const list = await call(
    mine
      ? api.DELETE('/messages/{id}/reactions/{emoji}', params)
      : api.PUT('/messages/{id}/reactions/{emoji}', params),
  );
  const cur = messages.find(m.channel_id, m.id);
  if (cur) cur.reactions = list.items;
}
