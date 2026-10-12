// Ephemeral command replies ("Only visible to you"). Never persisted: they
// live in memory and disappear on reload or dismissal.
import type { Message } from '../api/types';
import { onLocalReset } from '../reset';

export interface EphemeralNotice {
  key: string;
  invocationId: string;
  channelId: string;
  threadRootId: string | null;
  author: Message['author'] | null;
  body: string;
}

const MAX_NOTICES = 50;

class EphemeralStore {
  items = $state<EphemeralNotice[]>([]);

  /** Adds the notice from an ephemeral.message event payload. */
  add(data: Record<string, unknown>): void {
    const channelId = typeof data['channel_id'] === 'string' ? data['channel_id'] : '';
    const body = typeof data['body'] === 'string' ? data['body'] : '';
    if (!channelId || !body) return;
    const invocationId = typeof data['invocation_id'] === 'string' ? data['invocation_id'] : '';
    this.items.push({
      key: `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`,
      invocationId,
      channelId,
      threadRootId: typeof data['thread_root_id'] === 'string' ? data['thread_root_id'] : null,
      author: (data['author'] as Message['author'] | undefined | null) ?? null,
      body,
    });
    if (this.items.length > MAX_NOTICES) this.items.splice(0, this.items.length - MAX_NOTICES);
  }

  forScope(channelId: string, threadRootId: string | null = null): EphemeralNotice[] {
    return this.items.filter(
      (n) => n.channelId === channelId && (n.threadRootId ?? null) === threadRootId,
    );
  }

  dismiss(key: string): void {
    const i = this.items.findIndex((n) => n.key === key);
    if (i !== -1) this.items.splice(i, 1);
  }

  clear(): void {
    this.items = [];
  }
}

export const ephemerals = new EphemeralStore();

onLocalReset(() => ephemerals.clear());
