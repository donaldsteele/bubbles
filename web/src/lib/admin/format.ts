/** Groups a SHA-256 fingerprint (hex, any separators) by 4 characters: "AB12 CD34 ...". */
export function groupFingerprint(fp: string): string {
  const hex = fp
    .replace(/^sha256:/i, '')
    .replace(/[^0-9a-fA-F]/g, '')
    .toUpperCase();
  return hex.replace(/(.{4})(?=.)/g, '$1 ');
}

const UNITS = ['B', 'KB', 'MB', 'GB', 'TB'];

export function formatBytes(n: number): string {
  let v = Math.max(0, n);
  let i = 0;
  while (v >= 1024 && i < UNITS.length - 1) {
    v /= 1024;
    i++;
  }
  return `${i === 0 ? v : v.toFixed(v >= 100 ? 0 : 1)} ${UNITS[i]}`;
}

/** "3d 4h", "2h 5m", "45s": the two largest non-zero units. */
export function formatUptime(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  const parts: [number, string][] = [
    [Math.floor(s / 86400), 'd'],
    [Math.floor((s % 86400) / 3600), 'h'],
    [Math.floor((s % 3600) / 60), 'm'],
    [s % 60, 's'],
  ];
  const used = parts.filter(([n]) => n > 0).slice(0, 2);
  return used.length === 0 ? '0s' : used.map(([n, u]) => `${n}${u}`).join(' ');
}

/** Local date and time of an API timestamp; '' when absent. */
export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString();
}

/** Whole days from now until iso (negative when past). */
export function daysUntil(iso: string, now: number = Date.now()): number {
  return Math.floor((new Date(iso).getTime() - now) / 86_400_000);
}
