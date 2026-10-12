export interface Edit {
  value: string;
  start: number;
  end: number;
}

/** Wraps the selection in a Markdown marker; with no selection inserts a pair. */
export function wrapSelection(value: string, start: number, end: number, marker: string): Edit {
  const m = marker.length;
  const before = value.slice(0, start);
  const sel = value.slice(start, end);
  const after = value.slice(end);
  if (sel.startsWith(marker) && sel.endsWith(marker) && sel.length >= 2 * m) {
    const inner = sel.slice(m, sel.length - m);
    return { value: before + inner + after, start, end: start + inner.length };
  }
  return { value: before + marker + sel + marker + after, start: start + m, end: end + m };
}

/** Map a shortcut key to its Markdown marker. */
export function markerFor(key: string): string | null {
  switch (key.toLowerCase()) {
    case 'b':
      return '**';
    case 'i':
      return '_';
    case 'e':
      return '`';
    default:
      return null;
  }
}
