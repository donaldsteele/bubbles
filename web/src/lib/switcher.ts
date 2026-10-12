// Quick switcher: fuzzy matching of channel and direct-message labels.

/** Subsequence score: lower is better, null when the query does not match. */
export function fuzzyScore(query: string, label: string): number | null {
  const q = query.toLowerCase().replace(/^[#@]/, '');
  const l = label.toLowerCase();
  if (q === '') return 0;
  const idx = l.indexOf(q);
  if (idx !== -1) return idx === 0 ? 0 : 1 + idx / 100;
  let pos = -1;
  let gaps = 0;
  for (const ch of q) {
    const at = l.indexOf(ch, pos + 1);
    if (at === -1) return null;
    gaps += at - pos - 1;
    pos = at;
  }
  return 10 + gaps / 100;
}

export interface SwitcherEntry {
  id: string;
  label: string;
  dm: boolean;
}

/** Best matches first; stable for equal scores. */
export function rankEntries(query: string, entries: SwitcherEntry[], limit = 12): SwitcherEntry[] {
  return entries
    .map((e, i) => ({ e, i, s: fuzzyScore(query, e.label) }))
    .filter((x): x is { e: SwitcherEntry; i: number; s: number } => x.s !== null)
    .sort((a, b) => a.s - b.s || a.i - b.i)
    .slice(0, limit)
    .map((x) => x.e);
}
