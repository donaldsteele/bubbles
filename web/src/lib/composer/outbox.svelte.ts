// Optimistic sends. Each message gets a client nonce when it is queued; every
// (re)send uses the same nonce so the server stores it once.
import { api, call, ApiError } from '../api/client';
import type { Message } from '../api/types';
import { onLocalReset } from '../reset';
import { messages } from '../state/messages.svelte';

export type OutboxStatus = 'pending' | 'sending' | 'failed';

export interface OutboxItem {
  nonce: string;
  channelId: string;
  threadRootId: string | null;
  body: string;
  /** Uploaded files attached to the message. */
  fileIds: string[];
  status: OutboxStatus;
  attempts: number;
  createdAt: string;
}

export type Sender = (item: OutboxItem) => Promise<Message>;

const CROCKFORD = '0123456789ABCDEFGHJKMNPQRSTVWXYZ';

/** 26-char ULID-like nonce: 48-bit time then 80 random bits (crypto source). */
export function newNonce(now = Date.now()): string {
  let t = now;
  let out = '';
  for (let i = 0; i < 10; i++) {
    out = CROCKFORD.charAt(t % 32) + out;
    t = Math.floor(t / 32);
  }
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  for (const b of bytes) out += CROCKFORD.charAt(b & 31);
  return out;
}

/** Network errors keep the message pending this many times before it fails. */
export const MAX_NETWORK_ATTEMPTS = 3;

export class Outbox {
  items = $state<OutboxItem[]>([]);
  #send: Sender;
  #onSent: (m: Message, item: OutboxItem) => void;

  constructor(send: Sender, onSent: (m: Message, item: OutboxItem) => void = () => {}) {
    this.#send = send;
    this.#onSent = onSent;
  }

  enqueue(
    channelId: string,
    body: string,
    threadRootId: string | null = null,
    nonce = newNonce(),
    fileIds: string[] = [],
  ): OutboxItem {
    const item: OutboxItem = {
      nonce,
      channelId,
      threadRootId,
      body,
      fileIds,
      status: 'pending',
      attempts: 0,
      // eslint-disable-next-line svelte/prefer-svelte-reactivity -- timestamp value
      createdAt: new Date().toISOString(),
    };
    this.items.push(item);
    return this.items[this.items.length - 1] as OutboxItem;
  }

  forScope(channelId: string, threadRootId: string | null = null): OutboxItem[] {
    return this.items.filter(
      (i) => i.channelId === channelId && (i.threadRootId ?? null) === threadRootId,
    );
  }

  find(nonce: string): OutboxItem | undefined {
    return this.items.find((i) => i.nonce === nonce);
  }

  /** Sends one item. Success removes it; failure keeps it with a status. */
  async deliver(nonce: string): Promise<void> {
    const item = this.find(nonce);
    if (!item || item.status === 'sending') return;
    item.status = 'sending';
    item.attempts += 1;
    try {
      const m = await this.#send(item);
      this.remove(nonce);
      this.#onSent(m, item);
    } catch (err) {
      const network = err instanceof ApiError && err.status === 0;
      item.status = network && item.attempts < MAX_NETWORK_ATTEMPTS ? 'pending' : 'failed';
    }
  }

  /** Re-sends everything still pending (after a reconnect), oldest first, same nonces. */
  async flush(): Promise<void> {
    for (const n of this.items.filter((i) => i.status === 'pending').map((i) => i.nonce)) {
      await this.deliver(n);
    }
  }

  /** Manual retry of a failed item, same nonce. */
  async retry(nonce: string): Promise<void> {
    const item = this.find(nonce);
    if (!item || item.status === 'sending') return;
    item.status = 'pending';
    item.attempts = 0;
    await this.deliver(nonce);
  }

  /**
   * An incoming message.created by the signed-in user with the same body in
   * the same scope means an earlier send actually arrived (for example the
   * response was lost). Drops the matching pending or failed rows.
   */
  reconcile(m: Pick<Message, 'channel_id' | 'thread_root_id' | 'body'>): void {
    const keep = this.items.filter(
      (i) =>
        !(
          i.channelId === m.channel_id &&
          (i.threadRootId ?? null) === (m.thread_root_id ?? null) &&
          i.body === m.body &&
          i.status !== 'sending'
        ),
    );
    if (keep.length !== this.items.length) this.items = keep;
  }

  remove(nonce: string): void {
    const i = this.items.findIndex((x) => x.nonce === nonce);
    if (i !== -1) this.items.splice(i, 1);
  }

  clear(): void {
    this.items = [];
  }
}

async function sendViaApi(item: OutboxItem): Promise<Message> {
  return call(
    api.POST('/channels/{id}/messages', {
      params: { path: { id: item.channelId } },
      body: {
        body: item.body,
        nonce: item.nonce,
        ...(item.fileIds.length > 0 ? { file_ids: item.fileIds } : {}),
        ...(item.threadRootId ? { thread_root_id: item.threadRootId } : {}),
      },
    }),
  );
}

export const outbox = new Outbox(sendViaApi, (m) => messages.upsert(m));

onLocalReset(() => outbox.clear());
