import { execFileSync } from 'node:child_process';
import { existsSync, mkdtempSync, readdirSync, rmSync, statSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

// Builds the server (with the e2e test hooks) and the two example programs
// once per run. Workers find the binaries through BUBBLES_E2E_DIR.
const repoRoot = path.resolve(import.meta.dirname, '..', '..', '..', '..');
const exe = process.platform === 'win32' ? '.exe' : '';

function build(args: string[], out: string, pkg: string): void {
  execFileSync('go', ['build', ...args, '-o', out, pkg], {
    cwd: repoRoot,
    env: { ...process.env, CGO_ENABLED: '0' },
    stdio: 'inherit',
  });
}

function newestMtime(dir: string): number {
  let newest = 0;
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    newest = Math.max(newest, e.isDirectory() ? newestMtime(p) : statSync(p).mtimeMs);
  }
  return newest;
}

// The server embeds the built SPA, so a missing or older build is rebuilt.
function webBuildStale(): boolean {
  const index = path.join(repoRoot, 'internal', 'webui', 'dist', 'index.html');
  if (!existsSync(index)) return true;
  return newestMtime(path.join(repoRoot, 'web', 'src')) > statSync(index).mtimeMs;
}

export default function globalSetup(): () => void {
  if (webBuildStale()) {
    execFileSync('npm', ['--prefix', 'web', 'run', 'build'], {
      cwd: repoRoot,
      stdio: 'inherit',
      shell: true,
    });
  }
  const dir = mkdtempSync(path.join(os.tmpdir(), 'bubbles-e2e-bin-'));
  build(['-tags', 'e2e'], path.join(dir, `bubbles${exe}`), './cmd/bubbles');
  build([], path.join(dir, `netdiag-sender${exe}`), './examples/netdiag-sender');
  build([], path.join(dir, `echo-bot${exe}`), './examples/echo-bot');
  process.env.BUBBLES_E2E_DIR = dir;
  return () => {
    rmSync(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 });
  };
}
