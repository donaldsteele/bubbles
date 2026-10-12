// Guarded localStorage helpers. Private mode or a full quota must never break
// the UI, so every access is wrapped in try/catch.
export const PREFIX = 'bubbles:';

export const KEYS = {
  instance: 'bubbles:instance',
  settings: (viewer: string) => `bubbles:settings:${viewer}`,
  draftPrefix: 'bubbles:draft:',
  cachePrefix: 'bubbles:cache:',
} as const;

export function readItem(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function writeItem(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // storage unavailable or full: continue without persistence
  }
}

export function removeItem(key: string): void {
  try {
    localStorage.removeItem(key);
  } catch {
    // ignore
  }
}

/** Removes every key in the chosen store that starts with one of `prefixes`. */
export function removeByPrefix(prefixes: string[], store: 'local' | 'session' = 'local'): void {
  try {
    const s = store === 'local' ? localStorage : sessionStorage;
    const doomed: string[] = [];
    for (let i = 0; i < s.length; i++) {
      const k = s.key(i);
      if (k !== null && prefixes.some((p) => k.startsWith(p))) doomed.push(k);
    }
    for (const k of doomed) s.removeItem(k);
  } catch {
    // ignore
  }
}

export interface StoredInstance {
  instance_id: string;
  epoch: number;
}

export function readInstance(): StoredInstance | null {
  const raw = readItem(KEYS.instance);
  if (raw === null) return null;
  try {
    const v = JSON.parse(raw) as Partial<StoredInstance>;
    if (typeof v.instance_id === 'string' && typeof v.epoch === 'number') {
      return { instance_id: v.instance_id, epoch: v.epoch };
    }
  } catch {
    // fall through
  }
  return null;
}

export function writeInstance(v: StoredInstance): void {
  writeItem(KEYS.instance, JSON.stringify(v));
}
