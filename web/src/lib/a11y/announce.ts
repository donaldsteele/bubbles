// Text and throttling helpers for the live regions.
import type { Message } from '../api/types';
import { fmt, t } from '../i18n/en';

/** Minimum gap between two typing announcements. */
export const TYPING_ANNOUNCE_GAP_MS = 10_000;

export function typingPhrase(names: string[]): string {
  if (names.length === 0) return '';
  if (names.length === 1) return fmt(t.chat.typingOne, { a: names[0] as string });
  if (names.length === 2) {
    return fmt(t.chat.typingTwo, { a: names[0] as string, b: names[1] as string });
  }
  return t.chat.typingMany;
}

/** Summary for a batch of new messages: "3 new messages from Ana". */
export function summarizeNew(batch: Message[], nameOf: (m: Message) => string): string {
  if (batch.length === 0) return '';
  const names = [...new Set(batch.map(nameOf))];
  if (names.length > 1) return fmt(t.chat.newFromMany, { n: String(batch.length) });
  const name = names[0] as string;
  return batch.length === 1
    ? fmt(t.chat.newFromOne, { name })
    : fmt(t.chat.newFrom, { n: String(batch.length), name });
}

/** Allows an action at most once per gap. */
export class RateGate {
  #last = Number.NEGATIVE_INFINITY;
  constructor(private gapMs: number) {}

  tryPass(now: number): boolean {
    if (now - this.#last < this.gapMs) return false;
    this.#last = now;
    return true;
  }
}
