// Routes a WebSocket event to the state modules. Each branch is idempotent so a
// replayed or duplicated frame is harmless.
import type { Channel, Membership, Message, User } from '../api/types';
import { outbox } from '../composer/outbox.svelte';
import { handleIncoming } from '../notify/handler';
import { currentRoute } from '../router';
import type { WsEvent } from '../ws/client';
import { channels } from './channels.svelte';
import { ephemerals } from './ephemeral.svelte';
import { messages } from './messages.svelte';
import { presence, type PresenceState } from './presence.svelte';
import { session } from './session.svelte';
import { unread } from './unread.svelte';
import { users } from './users.svelte';

export interface DispatchHooks {
  /** settings.updated: clients re-fetch. */
  onSettingsUpdated?: () => void;
}

export const dispatchHooks: DispatchHooks = {};

interface ChannelEventData {
  channel?: Channel;
  memberships?: Membership[];
  membership?: Membership;
  user_id?: string;
  channel_id?: string;
}

export function applyEvent(ev: WsEvent): void {
  const data = ev.data;
  const meId = session.me?.id;
  switch (ev.type) {
    case 'message.created': {
      const m = data as unknown as Message;
      const existed = messages.find(m.channel_id, m.id) !== undefined;
      messages.applyEvent(ev.type, ev.channel_id, data);
      // Same rule as the server's unread counts: thread replies count, system messages do not.
      if (!existed && m.author && m.author.id !== meId && m.kind !== 'system' && !m.deleted) {
        const men = m.mentions;
        const mentioned =
          !!men && (men.channel || men.here || (!!meId && men.user_ids.includes(meId)));
        unread.bump(m.channel_id, mentioned);
      }
      if (m.author && m.author.id === meId) outbox.reconcile(m);
      if (!existed && m.author && m.author.id !== meId) {
        handleIncoming(m, 'created', currentRoute().params['channelId']);
      }
      return;
    }
    case 'message.updated':
    case 'message.deleted':
    case 'reaction.added':
    case 'reaction.removed':
    case 'pin.added':
    case 'pin.removed':
      messages.applyEvent(ev.type, ev.channel_id, data);
      return;
    case 'alert.updated': {
      const d = data as { message?: Message; transition?: string };
      if (d.message) {
        messages.upsert(d.message);
        if (d.message.author.id !== meId && d.transition) {
          handleIncoming(d.message, d.transition, currentRoute().params['channelId']);
        }
      }
      return;
    }
    case 'ephemeral.message':
      ephemerals.add(data);
      return;
    case 'channel.created':
    case 'channel.updated':
    case 'channel.archived':
    case 'channel.unarchived': {
      const d = data as ChannelEventData;
      if (d.channel) channels.upsert(d.channel);
      for (const mem of d.memberships ?? []) {
        if (mem.user_id === meId) channels.setMembership(mem);
      }
      return;
    }
    case 'channel.member_joined': {
      const d = data as ChannelEventData;
      if (d.channel) channels.upsert(d.channel);
      if (d.membership && d.membership.user_id === meId) channels.setMembership(d.membership);
      return;
    }
    case 'channel.member_left': {
      const d = data as ChannelEventData;
      if (d.user_id === meId && d.channel_id) channels.removeMembership(d.channel_id);
      return;
    }
    case 'membership.updated':
      channels.setMembership(data as unknown as Membership);
      return;
    case 'read.updated': {
      const d = data as { channel_id?: string; unread?: number; mentions?: number };
      if (d.channel_id) unread.set(d.channel_id, d.unread ?? 0, d.mentions ?? 0);
      return;
    }
    case 'user.updated':
      users.upsert(data as unknown as User);
      return;
    case 'presence.changed': {
      const d = data as { user_id?: string; state?: PresenceState };
      if (d.user_id && d.state) presence.setState(d.user_id, d.state);
      return;
    }
    case 'typing': {
      const d = data as { channel_id?: string; user_id?: string };
      const ch = d.channel_id ?? ev.channel_id;
      if (ch && d.user_id) presence.noteTyping(ch, d.user_id);
      return;
    }
    case 'settings.updated':
      dispatchHooks.onSettingsUpdated?.();
      return;
    default:
      // session.revoked (the socket closes 4009), command.invoked
      // and future types are handled elsewhere or ignored.
      return;
  }
}
