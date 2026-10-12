// Pure row building for the message list: grouping, day dividers, "New" divider.
import type { Message } from '../api/types';

export const GROUP_WINDOW_MS = 5 * 60 * 1000;

export type Row =
  | { type: 'day'; key: string; day: string; ts: number }
  | { type: 'new'; key: string }
  | { type: 'msg'; key: string; message: Message; grouped: boolean };

/** Local calendar day key (YYYY-MM-DD) of an RFC 3339 timestamp. */
export function dayKey(iso: string): string {
  const d = new Date(iso);
  const mm = String(d.getMonth() + 1).padStart(2, '0');
  const dd = String(d.getDate()).padStart(2, '0');
  return `${d.getFullYear()}-${mm}-${dd}`;
}

function sender(m: Message): string {
  return `${m.author.id}|${m.author.kind === 'app' ? (m.override_name ?? '') : ''}`;
}

export interface RowOptions {
  /** Message id of the last read message; the divider goes before the next one. */
  lastReadId?: string | null;
  /** Signed-in user; their own messages never start the "New" divider. */
  meId?: string;
}

export function buildRows(items: Message[], opts: RowOptions = {}): Row[] {
  const rows: Row[] = [];
  let prev: Message | null = null;
  let prevDay = '';
  let newPlaced = !opts.lastReadId;
  for (const m of items) {
    const day = dayKey(m.created_at);
    let broke = false;
    if (day !== prevDay) {
      rows.push({ type: 'day', key: `day:${day}`, day, ts: new Date(m.created_at).getTime() });
      prevDay = day;
      broke = true;
    }
    if (!newPlaced && opts.lastReadId && m.id > opts.lastReadId && m.author.id !== opts.meId) {
      rows.push({ type: 'new', key: 'new' });
      newPlaced = true;
      broke = true;
    }
    const grouped =
      !broke &&
      prev !== null &&
      !prev.deleted &&
      !m.deleted &&
      prev.kind === 'text' &&
      m.kind === 'text' &&
      sender(prev) === sender(m) &&
      new Date(m.created_at).getTime() - new Date(prev.created_at).getTime() <= GROUP_WINDOW_MS;
    rows.push({ type: 'msg', key: m.id, message: m, grouped });
    prev = m;
  }
  return rows;
}
