import type { Channel, Membership } from '../api/types';
import { onLocalReset } from '../reset';

class ChannelState {
  byId = $state<Record<string, Channel>>({});
  /** The signed-in user's memberships, by channel id. */
  memberships = $state<Record<string, Membership>>({});

  /** Channels the user belongs to, then the rest, by name. */
  get list(): Channel[] {
    return Object.values(this.byId).sort((a, b) => (a.name ?? a.id).localeCompare(b.name ?? b.id));
  }

  get joined(): Channel[] {
    return this.list.filter((c) => c.id in this.memberships && c.archived_at === null);
  }

  load(channels: Channel[], memberships: Membership[]): void {
    const c: Record<string, Channel> = {};
    for (const ch of channels) c[ch.id] = ch;
    const m: Record<string, Membership> = {};
    for (const mem of memberships) m[mem.channel_id] = mem;
    this.byId = c;
    this.memberships = m;
  }

  upsert(ch: Channel): void {
    this.byId[ch.id] = ch;
  }

  setMembership(m: Membership): void {
    this.memberships[m.channel_id] = m;
  }

  removeMembership(channelId: string): void {
    delete this.memberships[channelId];
  }

  clear(): void {
    this.byId = {};
    this.memberships = {};
  }
}

export const channels = new ChannelState();

onLocalReset(() => channels.clear());
