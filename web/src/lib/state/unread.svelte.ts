import type { Unread } from '../api/types';
import { onLocalReset } from '../reset';

export interface Counts {
  unread: number;
  mentions: number;
}

class UnreadState {
  byChannel = $state<Record<string, Counts>>({});

  get total(): number {
    return Object.values(this.byChannel).reduce((n, c) => n + c.unread, 0);
  }

  get totalMentions(): number {
    return Object.values(this.byChannel).reduce((n, c) => n + c.mentions, 0);
  }

  get(channelId: string): Counts {
    return this.byChannel[channelId] ?? { unread: 0, mentions: 0 };
  }

  load(items: Unread[]): void {
    const next: Record<string, Counts> = {};
    for (const u of items) next[u.channel_id] = { unread: u.unread, mentions: u.mentions };
    this.byChannel = next;
  }

  /** read.updated carries absolute counts, so applying it twice is harmless. */
  set(channelId: string, unread: number, mentions: number): void {
    this.byChannel[channelId] = { unread, mentions };
  }

  /** A new message from someone else in a channel that is not being read. */
  bump(channelId: string, mention: boolean): void {
    const c = this.get(channelId);
    this.byChannel[channelId] = {
      unread: c.unread + 1,
      mentions: c.mentions + (mention ? 1 : 0),
    };
  }

  clear(): void {
    this.byChannel = {};
  }
}

export const unread = new UnreadState();

onLocalReset(() => unread.clear());
