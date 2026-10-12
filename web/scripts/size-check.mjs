// Fails when the JavaScript needed for first paint exceeds 250 KB gzip.
// Initial JS = the entry script and the modulepreload chunks named in index.html;
// lazily imported chunks (emoji picker, overlays) are not counted.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { gzipSync } from 'node:zlib';

const LIMIT = 250 * 1024;
const dist = fileURLToPath(new URL('../../internal/webui/dist', import.meta.url));
const html = readFileSync(join(dist, 'index.html'), 'utf8');

const refs = new Set();
for (const m of html.matchAll(/<script[^>]*\ssrc="([^"]+\.js)"/g)) refs.add(m[1]);
for (const m of html.matchAll(/<link[^>]*rel="modulepreload"[^>]*\shref="([^"]+\.js)"/g)) {
  refs.add(m[1]);
}
if (refs.size === 0) {
  console.error('size-check: no scripts found in index.html');
  process.exit(1);
}

let total = 0;
for (const ref of refs) {
  const bytes = gzipSync(readFileSync(join(dist, ref.replace(/^\//, '')))).length;
  total += bytes;
  console.log(`size-check: ${ref} ${(bytes / 1024).toFixed(1)} KB gzip`);
}
console.log(`size-check: initial JS ${(total / 1024).toFixed(1)} KB gzip (limit 250 KB)`);
if (total > LIMIT) {
  console.error('size-check: initial JS exceeds the 250 KB gzip budget');
  process.exit(1);
}
