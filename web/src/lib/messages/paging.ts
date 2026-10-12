// Pure windowing helpers for the message list: merge pages, enforce the DOM cap.
import type { Message } from '../api/types';

/** The list never keeps more than this many message nodes. */
export const DOM_CAP = 400;

export interface Window {
  items: Message[];
  hasBefore: boolean;
  hasAfter: boolean;
}

function byId(a: Message, b: Message): number {
  return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
}

/** Merges a fetched page into the loaded items (dedupe by id, newest data wins). */
export function mergeItems(current: Message[], page: Message[]): Message[] {
  const map = new Map<string, Message>();
  for (const m of current) map.set(m.id, m);
  for (const m of page) {
    const cur = map.get(m.id);
    if (cur?.deleted && !m.deleted) continue;
    map.set(m.id, m);
  }
  return [...map.values()].sort(byId);
}

/**
 * Applies the DOM cap after a page was merged. Loading older messages drops the
 * newest ones (and sets hasAfter); loading newer ones drops the oldest.
 */
export function capWindow(w: Window, loaded: 'older' | 'newer', cap = DOM_CAP): Window {
  if (w.items.length <= cap) return w;
  if (loaded === 'older') {
    return { items: w.items.slice(0, cap), hasBefore: w.hasBefore, hasAfter: true };
  }
  return { items: w.items.slice(w.items.length - cap), hasBefore: true, hasAfter: w.hasAfter };
}
