import { spawn, type ChildProcess } from 'node:child_process';
import path from 'node:path';

const exe = process.platform === 'win32' ? '.exe' : '';

export interface Running {
  proc: ChildProcess;
  /** Resolves with the exit code. */
  done: Promise<number>;
  output: () => string;
  stop: () => Promise<void>;
}

/** Starts one of the example programs built by global setup. */
export function runExample(
  name: 'netdiag-sender' | 'echo-bot',
  args: string[],
  stdin?: string,
): Running {
  const dir = process.env.BUBBLES_E2E_DIR ?? '';
  const proc = spawn(path.join(dir, `${name}${exe}`), args, {
    stdio: [stdin === undefined ? 'ignore' : 'pipe', 'pipe', 'pipe'],
  });
  let out = '';
  proc.stdout?.on('data', (c: Buffer) => (out += c.toString()));
  proc.stderr?.on('data', (c: Buffer) => (out += c.toString()));
  if (stdin !== undefined) proc.stdin?.end(stdin);
  const done = new Promise<number>((resolve) => {
    proc.once('exit', (code) => resolve(code ?? -1));
  });
  return {
    proc,
    done,
    output: () => out,
    stop: async () => {
      if (proc.exitCode === null) proc.kill();
      await done;
    },
  };
}

/** Waits until the program printed text, or throws with its output. */
export async function waitForOutput(r: Running, text: string, timeoutMs = 20_000): Promise<void> {
  const end = Date.now() + timeoutMs;
  while (!r.output().includes(text)) {
    if (r.proc.exitCode !== null || Date.now() > end) {
      throw new Error(`expected "${text}" in output, got:\n${r.output()}`);
    }
    await new Promise((res) => setTimeout(res, 100));
  }
}
