import { ApiError, api, unwrap } from '../api/client';

/** File name from a Content-Disposition header; a dated default otherwise. */
export function backupFileName(header: string | null, now: Date = new Date()): string {
  const m = header ? /filename="?([^";]+)"?/i.exec(header) : null;
  const name = m?.[1]?.replace(/[^A-Za-z0-9._-]/g, '');
  if (name) return name;
  const p = (n: number) => String(n).padStart(2, '0');
  return `bubbles-${now.getFullYear()}${p(now.getMonth() + 1)}${p(now.getDate())}.bbk`;
}

/**
 * Asks the server for an age-encrypted backup (POST /admin/backup) and saves it
 * through the browser. The passphrase leaves the page only in that request.
 */
export async function downloadBackup(passphrase: string): Promise<void> {
  let res;
  try {
    res = await api.POST('/admin/backup', { body: { passphrase }, parseAs: 'blob' });
  } catch {
    throw new ApiError(0, 'network', 'network error');
  }
  const blob = unwrap(res) as unknown as Blob;
  const url = URL.createObjectURL(blob);
  try {
    const a = document.createElement('a');
    a.href = url;
    a.download = backupFileName(res.response.headers.get('Content-Disposition'));
    document.body.append(a);
    a.click();
    a.remove();
  } finally {
    setTimeout(() => URL.revokeObjectURL(url), 10_000);
  }
}
