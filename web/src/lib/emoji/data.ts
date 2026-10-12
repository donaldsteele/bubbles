// Emoji data: loaded lazily from the server's own /emoji/data.json (built from
// the pinned emojibase-data package), never from a CDN.
import { readItem, writeItem } from '../storage';

export interface EmojiItem {
  /** The emoji character(s). */
  e: string;
  /** Label, e.g. "grinning face". */
  n: string;
  /** Index into EmojiData.groups. */
  g: number;
  /** Shortcodes without colons. */
  s: string[];
  /** Search tags. */
  t: string[];
  /** Skin-tone variants for tones 1..5 when the emoji supports them. */
  k?: string[];
}

export interface EmojiData {
  groups: string[];
  items: EmojiItem[];
}

let loading: Promise<EmojiData> | null = null;

/** Fetches the emoji data once. A failed load can be retried. */
export function loadEmoji(fetchImpl: typeof fetch = fetch): Promise<EmojiData> {
  loading ??= fetchImpl('/emoji/data.json', { credentials: 'same-origin' })
    .then((r) => {
      if (!r.ok) throw new Error('emoji data unavailable');
      return r.json() as Promise<EmojiData>;
    })
    .catch((err: unknown) => {
      loading = null;
      throw err;
    });
  return loading;
}

/** Lower-cased words the item can be found by. */
function words(it: EmojiItem): string[] {
  return [...it.s, ...it.t, ...it.n.toLowerCase().split(/\s+/)].map((x) => x.toLowerCase());
}

/** Items matching a search: exact shortcode, shortcode prefix, then any word prefix. */
export function searchEmoji(data: EmojiData, query: string, limit = 50): EmojiItem[] {
  const q = query.trim().toLowerCase().replace(/^:/, '').replace(/:$/, '');
  if (q === '') return [];
  const exact: EmojiItem[] = [];
  const prefix: EmojiItem[] = [];
  const word: EmojiItem[] = [];
  for (const it of data.items) {
    if (it.s.some((s) => s === q)) exact.push(it);
    else if (it.s.some((s) => s.startsWith(q))) prefix.push(it);
    else if (words(it).some((w) => w.startsWith(q))) word.push(it);
  }
  return [...exact, ...prefix, ...word].slice(0, limit);
}

/** The emoji for an exact shortcode, if any. */
export function byShortcode(data: EmojiData, name: string): EmojiItem | undefined {
  const q = name.toLowerCase();
  return data.items.find((it) => it.s.includes(q));
}

export function applyTone(it: EmojiItem, tone: number): string {
  return tone >= 1 && tone <= 5 && it.k ? (it.k[tone - 1] ?? it.e) : it.e;
}

const RECENTS_KEY = 'bubbles:emoji:recents';
const TONE_KEY = 'bubbles:emoji:tone';
const MAX_RECENTS = 24;

export function loadRecents(): string[] {
  try {
    const v = JSON.parse(readItem(RECENTS_KEY) ?? '[]') as unknown;
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
  } catch {
    return [];
  }
}

export function pushRecent(emoji: string): string[] {
  const next = [emoji, ...loadRecents().filter((x) => x !== emoji)].slice(0, MAX_RECENTS);
  writeItem(RECENTS_KEY, JSON.stringify(next));
  return next;
}

export function loadTone(): number {
  const n = Number(readItem(TONE_KEY));
  return Number.isInteger(n) && n >= 0 && n <= 5 ? n : 0;
}

export function saveTone(tone: number): void {
  writeItem(TONE_KEY, String(tone));
}
