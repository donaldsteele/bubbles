// Fails when the built bundle references an external http(s) origin.
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const dist = fileURLToPath(new URL('../../internal/webui/dist', import.meta.url));
// Namespace identifiers and Svelte error-doc strings (never fetched) and loopback hosts are allowed.
const allowed = [
  /^https?:\/\/localhost/,
  /^https?:\/\/127\.0\.0\.1/,
  /^http:\/\/www\.w3\.org\//,
  /^https:\/\/svelte\.dev\/e\//,
  // Workbox error-message link inside sw.js; never fetched.
  /^https:\/\/bit\.ly\/wb-/,
];
const re = /https?:\/\/[A-Za-z0-9.[\]:-]+[^\s"'`)<>\\]*/g;

function walk(d) {
  return readdirSync(d).flatMap((n) => {
    const p = join(d, n);
    return statSync(p).isDirectory() ? walk(p) : [p];
  });
}

const bad = [];
for (const f of walk(dist).filter((p) => /\.(js|css|html)$/.test(p))) {
  for (const m of readFileSync(f, 'utf8').matchAll(re)) {
    if (!allowed.some((a) => a.test(m[0]))) bad.push(`${f}: ${m[0]}`);
  }
}
if (bad.length > 0) {
  console.error('check:origins: external origins in bundle:\n' + bad.join('\n'));
  process.exit(1);
}
console.log('check:origins: bundle references no external origins');
