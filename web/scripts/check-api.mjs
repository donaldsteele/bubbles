// Regenerates the API types into a temp file and fails when they differ from
// the committed src/lib/api/schema.d.ts.
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const committed = join(root, 'src/lib/api/schema.d.ts');
const dir = mkdtempSync(join(tmpdir(), 'bubbles-api-'));
const out = join(dir, 'schema.d.ts');
const bin = (n) => join(root, 'node_modules/.bin', process.platform === 'win32' ? `${n}.cmd` : n);
try {
  const opts = { cwd: root, stdio: 'pipe', shell: process.platform === 'win32' };
  execFileSync(bin('openapi-typescript'), ['../docs/openapi.yaml', '-o', out], opts);
  execFileSync(bin('prettier'), ['--write', '--config', join(root, '.prettierrc'), out], opts);
  const norm = (s) => s.replace(/\r\n/g, '\n');
  if (norm(readFileSync(out, 'utf8')) !== norm(readFileSync(committed, 'utf8'))) {
    console.error('check:api: src/lib/api/schema.d.ts is stale; run `npm run gen:api`');
    process.exitCode = 1;
  } else {
    console.log('check:api: schema.d.ts is up to date');
  }
} finally {
  rmSync(dir, { recursive: true, force: true });
}
