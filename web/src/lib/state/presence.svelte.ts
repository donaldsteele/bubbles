import { onLocalReset } from '../reset';

export type PresenceState = 'online' | 'idle' | 'offline';

/** The indicator expires this long after the last typing frame. */
export const TYPING_EXPIRY_MS = 6_000;

class PresenceStore {
  states = $state<Record<string, PresenceState>>({});
  /** channel id -> user id -> expiry (ms epoch). */
  typing = $state<Record<string, Record<string, number>>>({});

  stateOf(userId: string): PresenceState {
    return this.states[userId] ?? 'offline';
  }

  setState(userId: string, state: PresenceState): void {
    this.states[userId] = state;
  }

  noteTyping(channelId: string, userId: string, now = Date.now()): void {
    const ch = this.typing[channelId] ?? {};
    ch[userId] = now + TYPING_EXPIRY_MS;
    this.typing[channelId] = ch;
  }

  /** Users currently typing in a channel (expired entries are dropped). */
  typingIn(channelId: string, now = Date.now()): string[] {
    const ch = this.typing[channelId];
    if (!ch) return [];
    return Object.keys(ch).filter((u) => (ch[u] ?? 0) > now);
  }

  clearTyping(channelId: string, userId: string): void {
    const ch = this.typing[channelId];
    if (ch) delete ch[userId];
  }

  clear(): void {
    this.states = {};
    this.typing = {};
  }
}

export const presence = new PresenceStore();

onLocalReset(() => presence.clear());
