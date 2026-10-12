// Per-channel drafts in localStorage. Every access is guarded: private mode or
// a full disk must never break typing.
import { KEYS, readItem, removeItem, writeItem } from '../storage';

export function draftKey(channelId: string, threadRootId?: string | null): string {
  return KEYS.draftPrefix + (threadRootId ? `${channelId}:${threadRootId}` : channelId);
}

export function loadDraft(channelId: string, threadRootId?: string | null): string {
  try {
    return readItem(draftKey(channelId, threadRootId)) ?? '';
  } catch {
    return '';
  }
}

/** Saves the draft; an empty text removes it. */
export function saveDraft(channelId: string, text: string, threadRootId?: string | null): void {
  try {
    const key = draftKey(channelId, threadRootId);
    if (text.trim() === '') removeItem(key);
    else writeItem(key, text);
  } catch {
    // storage unavailable: the draft just is not persisted
  }
}

export function clearDraft(channelId: string, threadRootId?: string | null): void {
  try {
    removeItem(draftKey(channelId, threadRootId));
  } catch {
    // ignore
  }
}
