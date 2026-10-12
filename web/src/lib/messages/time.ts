import { fmt, t } from '../i18n/en';

/** "just now", "2 min ago", "3 h ago", "5 d ago". */
export function relTime(iso: string, now = Date.now()): string {
  const s = Math.max(0, Math.floor((now - new Date(iso).getTime()) / 1000));
  if (s < 60) return t.chat.justNow;
  const m = Math.floor(s / 60);
  if (m < 60) return fmt(t.chat.minAgo, { n: String(m) });
  const h = Math.floor(m / 60);
  if (h < 24) return fmt(t.chat.hourAgo, { n: String(h) });
  return fmt(t.chat.dayAgo, { n: String(Math.floor(h / 24)) });
}

export function clockTime(iso: string): string {
  return new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

/** Label for a day divider: Today, Yesterday or a long date. */
export function dayLabel(ts: number, now = Date.now()): string {
  const d = new Date(ts);
  const n = new Date(now);
  const start = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diff = Math.round((start(n) - start(d)) / 86_400_000);
  if (diff === 0) return t.chat.today;
  if (diff === 1) return t.chat.yesterday;
  return d.toLocaleDateString([], {
    weekday: 'long',
    year: 'numeric',
    month: 'long',
    day: 'numeric',
  });
}
