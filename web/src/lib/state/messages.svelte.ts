import type { Message } from '../api/types';
import { onLocalReset } from '../reset';

/** Event payload for reaction.added / reaction.removed. */
interface ReactionData {
  message_id?: string;
  channel_id?: string;
  user_id?: string;
  emoji?: string;
  count?: number;
  deleted?: boolean;
}

/** Event payload for pin.added / pin.removed. */
interface PinData {
  message_id?: string;
  channel_id?: string;
  deleted?: boolean;
}

/** Event payload for message.deleted. */
interface DeletedData {
  id?: string;
  channel_id?: string;
  thread_root_id?: string | null;
}

function insertSorted(list: Message[], m: Message): void {
  // ULIDs sort lexicographically in commit order. New messages usually go last.
  let i = list.length;
  while (i > 0 && (list[i - 1] as Message).id > m.id) i--;
  list.splice(i, 0, m);
}

/**
 * Messages per channel, ordered by id. Top-level messages live in `byChannel`,
 * thread replies in `threads` keyed by the root id. Every operation is
 * idempotent: replays and duplicate frames leave the state unchanged.
 */
class MessageStore {
  byChannel = $state<Record<string, Message[]>>({});
  threads = $state<Record<string, Message[]>>({});
  /** channel id -> pinned message ids. */
  pins = $state<Record<string, string[]>>({});

  list(channelId: string): Message[] {
    return this.byChannel[channelId] ?? [];
  }

  thread(rootId: string): Message[] {
    return this.threads[rootId] ?? [];
  }

  #bucket(m: Pick<Message, 'channel_id' | 'thread_root_id'>): Message[] {
    // Read back through the proxy: the value of an assignment expression is the raw array.
    const map = m.thread_root_id ? this.threads : this.byChannel;
    const key = m.thread_root_id ? m.thread_root_id : m.channel_id;
    if (!map[key]) map[key] = [];
    return map[key] as Message[];
  }

  find(channelId: string, id: string): Message | undefined {
    const top = this.byChannel[channelId]?.find((m) => m.id === id);
    if (top) return top;
    for (const replies of Object.values(this.threads)) {
      const r = replies.find((m) => m.id === id && m.channel_id === channelId);
      if (r) return r;
    }
    return undefined;
  }

  /** Inserts or replaces by id. A tombstone is never resurrected by a stale update. */
  upsert(m: Message): void {
    const bucket = this.#bucket(m);
    const i = bucket.findIndex((x) => x.id === m.id);
    if (i === -1) {
      insertSorted(bucket, m);
      return;
    }
    const cur = bucket[i] as Message;
    if (cur.deleted && !m.deleted) return;
    bucket[i] = m;
  }

  /** Marks a message deleted in place. Unknown ids are ignored. */
  tombstone(channelId: string, id: string): void {
    const m = this.find(channelId, id);
    if (!m || m.deleted) return;
    m.deleted = true;
    m.body = '';
    m.files = [];
    m.reactions = [];
    m.alert = null;
    this.#unpin(channelId, id);
  }

  applyReaction(type: 'reaction.added' | 'reaction.removed', d: ReactionData): void {
    if (!d.channel_id || !d.message_id) return;
    if (d.deleted) {
      this.tombstone(d.channel_id, d.message_id);
      return;
    }
    const m = this.find(d.channel_id, d.message_id);
    if (!m || m.deleted || !d.emoji || !d.user_id) return;
    const idx = m.reactions.findIndex((r) => r.emoji === d.emoji);
    const cur = idx === -1 ? undefined : m.reactions[idx];
    if (type === 'reaction.added') {
      if (cur) {
        if (!cur.user_ids.includes(d.user_id)) cur.user_ids.push(d.user_id);
        cur.count = d.count ?? cur.user_ids.length;
      } else {
        m.reactions.push({ emoji: d.emoji, count: d.count ?? 1, user_ids: [d.user_id] });
      }
    } else if (cur) {
      cur.user_ids = cur.user_ids.filter((u) => u !== d.user_id);
      cur.count = d.count ?? cur.user_ids.length;
      if (cur.count <= 0) m.reactions.splice(idx, 1);
    }
  }

  applyPin(type: 'pin.added' | 'pin.removed', d: PinData): void {
    if (!d.channel_id || !d.message_id) return;
    if (d.deleted || type === 'pin.removed') {
      this.#unpin(d.channel_id, d.message_id);
      return;
    }
    if (!this.pins[d.channel_id]) this.pins[d.channel_id] = [];
    const list = this.pins[d.channel_id] as string[];
    if (!list.includes(d.message_id)) list.push(d.message_id);
  }

  #unpin(channelId: string, id: string): void {
    const list = this.pins[channelId];
    if (list) this.pins[channelId] = list.filter((x) => x !== id);
  }

  /** Applies a message.* / reaction.* / pin.* event. Returns true when handled. */
  applyEvent(type: string, channelId: string | undefined, data: Record<string, unknown>): boolean {
    switch (type) {
      case 'message.created':
      case 'message.updated':
        this.upsert(data as unknown as Message);
        return true;
      case 'message.deleted': {
        const d = data as DeletedData;
        const ch = d.channel_id ?? channelId;
        if (ch && d.id) this.tombstone(ch, d.id);
        return true;
      }
      case 'reaction.added':
      case 'reaction.removed':
        this.applyReaction(type, { channel_id: channelId, ...(data as ReactionData) });
        return true;
      case 'pin.added':
      case 'pin.removed':
        this.applyPin(type, { channel_id: channelId, ...(data as PinData) });
        return true;
      default:
        return false;
    }
  }

  /** Replaces a channel's loaded page (REST fetch). */
  setChannelPage(channelId: string, items: Message[]): void {
    const sorted = [...items].sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
    this.byChannel[channelId] = [];
    for (const m of sorted) this.upsert(m);
  }

  clear(): void {
    this.byChannel = {};
    this.threads = {};
    this.pins = {};
  }
}

export const messages = new MessageStore();

onLocalReset(() => messages.clear());
