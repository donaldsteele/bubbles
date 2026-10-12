// Composer autocomplete: trigger detection, suggestions and listbox keys.
import type { Channel, User } from '../api/types';
import { searchEmoji, type EmojiData } from '../emoji/data';

export type TriggerKind = 'emoji' | 'user' | 'channel' | 'command';

export interface Trigger {
  kind: TriggerKind;
  /** Text typed after the trigger character. */
  query: string;
  /** Index of the trigger character. */
  start: number;
  /** Caret position (end of the replaced range). */
  end: number;
}

export interface Suggestion {
  key: string;
  label: string;
  detail: string;
  /** Text that replaces the trigger and the query. */
  insert: string;
}

export interface CommandInfo {
  name: string;
  description: string;
  usage: string;
}

const QUERY_CHARS: Record<string, RegExp> = {
  ':': /^[A-Za-z0-9_+-]*$/,
  '@': /^[A-Za-z0-9._-]*$/,
  '#': /^[A-Za-z0-9_-]*$/,
  '/': /^[A-Za-z0-9_-]*$/,
};
const KINDS: Record<string, TriggerKind> = {
  ':': 'emoji',
  '@': 'user',
  '#': 'channel',
  '/': 'command',
};

/** The trigger the caret is inside, if any. */
export function findTrigger(text: string, caret: number): Trigger | null {
  let i = caret - 1;
  while (i >= 0) {
    const ch = text.charAt(i);
    if (/\s/.test(ch)) return null;
    const re = QUERY_CHARS[ch];
    if (re) {
      const query = text.slice(i + 1, caret);
      const atWordStart = i === 0 || /\s/.test(text.charAt(i - 1));
      if (!atWordStart || !re.test(query)) return null;
      const kind = KINDS[ch] as TriggerKind;
      if (kind === 'command' && i !== 0) return null;
      if (kind === 'emoji' && query.length < 2) return null;
      return { kind, query, start: i, end: caret };
    }
    i--;
  }
  return null;
}

export interface SuggestContext {
  users: User[];
  channels: Channel[];
  commands: CommandInfo[];
  emoji: EmojiData | null;
}

const LIMIT = 8;

export function suggestionsFor(trigger: Trigger, ctx: SuggestContext): Suggestion[] {
  const q = trigger.query.toLowerCase();
  switch (trigger.kind) {
    case 'user': {
      const out: Suggestion[] = [];
      for (const kw of ['channel', 'here']) {
        if (kw.startsWith(q)) {
          out.push({ key: `kw-${kw}`, label: `@${kw}`, detail: '', insert: `@${kw} ` });
        }
      }
      for (const u of ctx.users) {
        if (u.deactivated) continue;
        if (u.username.toLowerCase().startsWith(q) || u.display_name.toLowerCase().includes(q)) {
          out.push({
            key: u.id,
            label: `@${u.username}`,
            detail: u.display_name,
            insert: `@${u.username} `,
          });
        }
      }
      return out.slice(0, LIMIT);
    }
    case 'channel':
      return ctx.channels
        .filter((c) => c.name && (c.kind === 'public' || c.kind === 'private'))
        .filter((c) => (c.name ?? '').toLowerCase().includes(q))
        .slice(0, LIMIT)
        .map((c) => ({
          key: c.id,
          label: `#${c.name ?? ''}`,
          detail: c.topic,
          insert: `#${c.name ?? ''} `,
        }));
    case 'command':
      return ctx.commands
        .filter((c) => c.name.toLowerCase().startsWith(q))
        .slice(0, LIMIT)
        .map((c) => ({
          key: c.name,
          label: `/${c.name}`,
          detail: c.usage || c.description,
          insert: `/${c.name} `,
        }));
    case 'emoji':
      return ctx.emoji
        ? searchEmoji(ctx.emoji, q, LIMIT).map((it) => ({
            key: it.e,
            label: `${it.e} :${it.s[0] ?? it.n}:`,
            detail: it.n,
            insert: it.e,
          }))
        : [];
  }
}

export interface Applied {
  value: string;
  caret: number;
}

/** Replaces the trigger range with the suggestion text. */
export function applySuggestion(text: string, trigger: Trigger, insert: string): Applied {
  const value = text.slice(0, trigger.start) + insert + text.slice(trigger.end);
  return { value, caret: trigger.start + insert.length };
}

export type ListboxAction = 'none' | 'move' | 'accept' | 'close';

/** Keyboard model of the listbox: Up/Down wrap, Enter/Tab accept, Esc closes. */
export function listboxKey(
  key: string,
  active: number,
  count: number,
): { active: number; action: ListboxAction } {
  if (count === 0) return { active: 0, action: 'none' };
  switch (key) {
    case 'ArrowDown':
      return { active: (active + 1) % count, action: 'move' };
    case 'ArrowUp':
      return { active: (active - 1 + count) % count, action: 'move' };
    case 'Enter':
    case 'Tab':
      return { active, action: 'accept' };
    case 'Escape':
      return { active, action: 'close' };
    default:
      return { active, action: 'none' };
  }
}
