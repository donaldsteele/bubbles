// REST actions on channels. Each updates local state right away; the matching
// websocket events are idempotent and arrive later.
import { api, call } from './api/client';
import type { Channel, Membership } from './api/types';
import { channels } from './state/channels.svelte';
import { session } from './state/session.svelte';
import { unread } from './state/unread.svelte';

function ownMembership(channelId: string, role: Membership['role']): Membership {
  return {
    channel_id: channelId,
    user_id: session.me?.id ?? '',
    role,
    joined_at: new Date().toISOString(),
    last_read_id: null,
    notify_level: 'all',
    alert_min_severity: 'info',
    muted_until: null,
  };
}

export async function listPublicChannels(): Promise<Channel[]> {
  const r = await call(api.GET('/channels', { params: { query: { scope: 'public' } } }));
  return r.items;
}

export async function joinChannel(ch: Channel): Promise<void> {
  const m = await call(api.POST('/channels/{id}/join', { params: { path: { id: ch.id } } }));
  channels.upsert(ch);
  channels.setMembership(m);
}

export async function createChannel(
  name: string,
  kind: 'public' | 'private',
  topic: string,
): Promise<Channel> {
  const ch = await call(
    api.POST('/channels', { body: { name, kind, ...(topic ? { topic } : {}), member_ids: [] } }),
  );
  channels.upsert(ch);
  channels.setMembership(ownMembership(ch.id, 'owner'));
  return ch;
}

export async function startDm(userIds: string[]): Promise<Channel> {
  const ch = await call(api.POST('/dms', { body: { user_ids: userIds } }));
  channels.upsert(ch);
  if (!channels.memberships[ch.id]) channels.setMembership(ownMembership(ch.id, 'member'));
  return ch;
}

export async function setNotifyLevel(
  channelId: string,
  level: Membership['notify_level'],
): Promise<void> {
  const m = await call(
    api.PATCH('/channels/{id}/membership', {
      params: { path: { id: channelId } },
      body: { notify_level: level },
    }),
  );
  channels.setMembership(m);
}

/** Marks everything up to messageId read and applies the returned counters. */
export async function markRead(channelId: string, messageId: string): Promise<void> {
  const r = await call(
    api.POST('/channels/{id}/read', {
      params: { path: { id: channelId } },
      body: { message_id: messageId },
    }),
  );
  unread.set(channelId, r.unread, r.mentions);
  const cur = channels.memberships[channelId];
  if (cur) channels.setMembership({ ...cur, last_read_id: r.last_read_id });
}

/** Updates the signed-in user's membership settings (level, alert floor, mute). */
export async function updateMembership(
  channelId: string,
  patch: {
    notify_level?: Membership['notify_level'];
    alert_min_severity?: Membership['alert_min_severity'];
    muted_until?: string | null;
  },
): Promise<void> {
  const m = await call(
    api.PATCH('/channels/{id}/membership', { params: { path: { id: channelId } }, body: patch }),
  );
  channels.setMembership(m);
}
