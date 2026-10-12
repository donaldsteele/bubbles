/** Human-readable size: 1.4 MB, 312 KB, 12 B. */
export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024;
  let u = 0;
  while (v >= 1024 && u < units.length - 1) {
    v /= 1024;
    u++;
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[u]}`;
}

/** Download URL for a stored file (served as an attachment). */
export function downloadUrl(url: string): string {
  return url.includes('?') ? `${url}&download=1` : `${url}?download=1`;
}
