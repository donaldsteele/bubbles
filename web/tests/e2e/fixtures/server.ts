import { spawn, type ChildProcess } from 'node:child_process';
import { createHash, X509Certificate } from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import http from 'node:http';
import https from 'node:https';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import tls from 'node:tls';

export const SETUP_TOKEN = 'e2e7e2e7e2e7e2e7e2e7e2e7e2e7e2e7e2e7e2e7e2e7e2e7e2e7';
export const SESSION_COOKIE = '__Host-bubbles_session';
export const PASSWORD = 'Correct-Horse-Battery-9';
const exe = process.platform === 'win32' ? '.exe' : '';

export interface User {
  username: string;
  password: string;
  id: string;
  cookie: string;
}

export interface ApiOptions {
  as?: User | { bearer: string } | null;
  json?: unknown;
  body?: Buffer;
  contentType?: string;
  headers?: Record<string, string>;
}

export interface ApiResult {
  status: number;
  headers: http.IncomingHttpHeaders;
  body: Buffer;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  json: any;
}

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.once('error', reject);
    srv.listen(0, '127.0.0.1', () => {
      const addr = srv.address();
      const port = typeof addr === 'object' && addr ? addr.port : 0;
      srv.close(() => resolve(port));
    });
  });
}

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

function httpGet(url: string): Promise<{ status: number; body: Buffer }> {
  return new Promise((resolve, reject) => {
    http
      .get(url, (res) => {
        const parts: Buffer[] = [];
        res.on('data', (c: Buffer) => parts.push(c));
        res.on('end', () => resolve({ status: res.statusCode ?? 0, body: Buffer.concat(parts) }));
      })
      .on('error', reject);
  });
}

/** One bubbles process on random loopback ports with its own data directory. */
export class Server {
  readonly httpsPort: number;
  readonly httpPort: number;
  readonly dataDir: string;
  readonly origin: string;
  ca = '';
  spki = '';
  admin!: User;
  instance = 'Bubbles E2E';
  private proc: ChildProcess | null = null;
  private log: string[] = [];
  private agent: https.Agent | undefined;

  private constructor(httpsPort: number, httpPort: number, dataDir: string) {
    this.httpsPort = httpsPort;
    this.httpPort = httpPort;
    this.dataDir = dataDir;
    this.origin = `https://127.0.0.1:${httpsPort}`;
  }

  static async start(label = 'spec'): Promise<Server> {
    const dir = mkdtempSync(path.join(os.tmpdir(), `bubbles-e2e-${label.replace(/\W/g, '')}-`));
    const s = new Server(await freePort(), await freePort(), dir);
    await s.launch();
    return s;
  }

  get binDir(): string {
    const dir = process.env.BUBBLES_E2E_DIR;
    if (!dir) throw new Error('BUBBLES_E2E_DIR is not set; globalSetup did not run');
    return dir;
  }

  private async launch(): Promise<void> {
    const bin = path.join(this.binDir, `bubbles${exe}`);
    const proc = spawn(
      bin,
      [
        'serve',
        '--data-dir',
        this.dataDir,
        '--https-addr',
        `127.0.0.1:${this.httpsPort}`,
        '--http-addr',
        `127.0.0.1:${this.httpPort}`,
        '--no-mdns',
        '--allow-public',
      ],
      {
        env: { ...process.env, BUBBLES_TEST_SETUP_TOKEN: SETUP_TOKEN },
        stdio: ['ignore', 'ignore', 'pipe'],
      },
    );
    this.proc = proc;
    this.log = [];
    proc.stderr?.on('data', (c: Buffer) => {
      this.log.push(c.toString());
      if (this.log.length > 40) this.log.shift();
    });
    let exited = false;
    proc.once('exit', () => {
      exited = true;
    });

    const deadline = Date.now() + 60_000;
    for (;;) {
      if (exited) throw new Error(`bubbles exited during startup:\n${this.log.join('')}`);
      if (Date.now() > deadline)
        throw new Error(`bubbles did not become ready:\n${this.log.join('')}`);
      if (this.ca === '') {
        try {
          const r = await httpGet(`http://127.0.0.1:${this.httpPort}/ca.pem`);
          if (r.status === 200 && r.body.toString().includes('BEGIN CERTIFICATE')) {
            this.ca = r.body.toString();
            this.agent = new https.Agent({ ca: this.ca, keepAlive: false });
          }
        } catch {
          // the onboarding listener is not up yet
        }
      }
      if (this.ca !== '' && (await this.healthy())) break;
      await sleep(100);
    }
    this.spki = await this.leafSpki();
  }

  private healthy(): Promise<boolean> {
    return this.api('GET', '/healthz', { as: null }).then(
      (r) => r.status === 200,
      () => false,
    );
  }

  private leafSpki(): Promise<string> {
    return new Promise((resolve, reject) => {
      const sock = tls.connect(
        { host: '127.0.0.1', port: this.httpsPort, rejectUnauthorized: false },
        () => {
          const raw = sock.getPeerCertificate(true).raw;
          sock.end();
          const spki = new X509Certificate(raw).publicKey.export({ type: 'spki', format: 'der' });
          resolve(createHash('sha256').update(spki).digest('base64'));
        },
      );
      sock.once('error', reject);
    });
  }

  /** Kills the process and starts it again on the same data dir and ports. */
  async restart(): Promise<void> {
    await this.kill();
    await this.launch();
  }

  async kill(): Promise<void> {
    const proc = this.proc;
    this.proc = null;
    if (!proc || proc.exitCode !== null) return;
    await new Promise<void>((resolve) => {
      proc.once('exit', () => resolve());
      proc.kill();
    });
  }

  async stop(): Promise<void> {
    await this.kill();
    rmSync(this.dataDir, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
  }

  setupTokenFile(): string {
    return readFileSync(path.join(this.dataDir, 'setup-token.txt'), 'utf8').trim();
  }

  api(method: string, urlPath: string, opts: ApiOptions = {}): Promise<ApiResult> {
    const headers: Record<string, string> = { Origin: this.origin, ...opts.headers };
    let payload: Buffer | undefined = opts.body;
    if (opts.json !== undefined) {
      payload = Buffer.from(JSON.stringify(opts.json));
      headers['Content-Type'] = 'application/json';
    }
    if (opts.contentType) headers['Content-Type'] = opts.contentType;
    const as = opts.as;
    if (as && 'bearer' in as) headers.Authorization = `Bearer ${as.bearer}`;
    else if (as) headers.Cookie = `${SESSION_COOKIE}=${as.cookie}`;
    if (payload) headers['Content-Length'] = String(payload.length);
    return new Promise((resolve, reject) => {
      const req = https.request(
        {
          host: '127.0.0.1',
          port: this.httpsPort,
          path: urlPath,
          method,
          headers,
          agent: this.agent ?? false,
          rejectUnauthorized: this.ca !== '',
          ca: this.ca || undefined,
        },
        (res) => {
          const parts: Buffer[] = [];
          res.on('data', (c: Buffer) => parts.push(c));
          res.on('end', () => {
            const body = Buffer.concat(parts);
            let json: unknown = null;
            if ((res.headers['content-type'] ?? '').includes('json') && body.length > 0) {
              json = JSON.parse(body.toString());
            }
            resolve({ status: res.statusCode ?? 0, headers: res.headers, body, json });
          });
        },
      );
      req.on('error', reject);
      if (payload) req.write(payload);
      req.end();
    });
  }

  /** api() that throws unless the status is one of want. */
  async must(
    method: string,
    urlPath: string,
    want: number[],
    opts: ApiOptions = {},
  ): Promise<ApiResult> {
    const res = await this.api(method, urlPath, opts);
    if (!want.includes(res.status)) {
      throw new Error(
        `${method} ${urlPath}: status ${res.status}: ${res.body.toString().slice(0, 300)}`,
      );
    }
    return res;
  }

  private sessionFrom(r: ApiResult, username: string, password: string): User {
    const cookies = ([] as string[]).concat(r.headers['set-cookie'] ?? []);
    const hit = cookies.find((c) => c.startsWith(`${SESSION_COOKIE}=`));
    if (!hit) throw new Error('no session cookie in response');
    const cookie = hit.slice(SESSION_COOKIE.length + 1).split(';')[0] ?? '';
    const id = (r.json?.user?.id ?? r.json?.id ?? '') as string;
    return { username, password, id, cookie };
  }

  /** Completes first-run setup through the API and stores the admin. */
  async setupAdmin(username = 'admin', password = PASSWORD): Promise<User> {
    const r = await this.must('POST', '/api/v1/setup', [201], {
      as: null,
      json: {
        setup_token: SETUP_TOKEN,
        username,
        password,
        display_name: 'Ada Admin',
        instance_name: this.instance,
      },
    });
    this.admin = this.sessionFrom(r, username, password);
    return this.admin;
  }

  async login(username: string, password = PASSWORD): Promise<User> {
    const r = await this.must('POST', '/api/v1/auth/login', [200], {
      as: null,
      json: { username, password },
    });
    return this.sessionFrom(r, username, password);
  }

  async createInvite(
    admin: User,
    body: { role?: string; note?: string; max_uses?: number } = {},
  ): Promise<{ code: string; url: string; id: string }> {
    const r = await this.must('POST', '/api/v1/admin/invites', [201], { as: admin, json: body });
    const url = r.json.url as string;
    return { code: url.split('/invite/')[1] ?? '', url, id: r.json.id as string };
  }

  async acceptInvite(code: string, username: string, password = PASSWORD): Promise<User> {
    const r = await this.must('POST', `/api/v1/invites/${code}/accept`, [200, 201], {
      as: null,
      json: {
        username,
        password,
        display_name: username.charAt(0).toUpperCase() + username.slice(1),
      },
    });
    return this.sessionFrom(r, username, password);
  }

  /** Creates a member (or another role) through an invite. */
  async createUser(username: string, role = 'member'): Promise<User> {
    const inv = await this.createInvite(this.admin, { role });
    return this.acceptInvite(inv.code, username);
  }

  async createChannel(
    as: User,
    name: string,
    kind: 'public' | 'private' = 'public',
  ): Promise<{ id: string; name: string }> {
    const r = await this.must('POST', '/api/v1/channels', [200, 201], { as, json: { name, kind } });
    return { id: (r.json.id ?? r.json.channel?.id) as string, name };
  }

  async joinChannel(as: User, channelId: string): Promise<void> {
    await this.must('POST', `/api/v1/channels/${channelId}/join`, [200, 201, 204], { as });
  }

  async channelByName(as: User, name: string): Promise<{ id: string; name: string }> {
    const r = await this.must('GET', '/api/v1/channels', [200], { as });
    const list = (r.json.items ?? r.json.channels ?? r.json) as { id: string; name: string }[];
    const hit = list.find((c) => c.name === name);
    if (!hit) throw new Error(`channel ${name} not found`);
    return hit;
  }

  async postMessage(as: User | { bearer: string }, channelId: string, body: string) {
    const r = await this.must('POST', `/api/v1/channels/${channelId}/messages`, [200, 201], {
      as,
      json: { body },
    });
    return r.json;
  }

  /** Uploads a file (multipart) and returns the stored file record. */
  async uploadFile(as: User, name: string, mime: string, data: Buffer) {
    const boundary = `----e2e${Date.now().toString(16)}`;
    const head = Buffer.from(
      `--${boundary}\r\nContent-Disposition: form-data; name="file"; filename="${name}"\r\n` +
        `Content-Type: ${mime}\r\n\r\n`,
    );
    const body = Buffer.concat([head, data, Buffer.from(`\r\n--${boundary}--\r\n`)]);
    const r = await this.must('POST', '/api/v1/files', [201], {
      as,
      body,
      contentType: `multipart/form-data; boundary=${boundary}`,
    });
    return r.json as { id: string; name: string; url: string; meta_stripped: boolean };
  }

  /** Posts a message with attachments; returns the stored message. */
  async postWithFiles(as: User, channelId: string, body: string, fileIds: string[]) {
    const r = await this.must('POST', `/api/v1/channels/${channelId}/messages`, [200, 201], {
      as,
      json: { body, file_ids: fileIds },
    });
    return r.json;
  }

  async react(as: User, messageId: string, emoji: string): Promise<void> {
    await this.must(
      'PUT',
      `/api/v1/messages/${messageId}/reactions/${encodeURIComponent(emoji)}`,
      [200, 201, 204],
      { as },
    );
  }

  async pin(as: User, channelId: string, messageId: string): Promise<void> {
    await this.must('PUT', `/api/v1/channels/${channelId}/pins/${messageId}`, [200, 201, 204], {
      as,
    });
  }

  async messages(as: User, channelId: string) {
    const r = await this.must('GET', `/api/v1/channels/${channelId}/messages?limit=100`, [200], {
      as,
    });
    return (r.json.items ?? r.json.messages ?? r.json) as { id: string; body: string }[];
  }

  /**
   * Creates an app with a token of the given scopes and puts its bot user into
   * the given channels (a bot only acts in channels it is a member of).
   */
  async createAppToken(
    admin: User,
    appName: string,
    scopes: string[],
    memberOf: string[] = [],
  ): Promise<{ token: string; appId: string; botId: string; connection: string }> {
    const app = await this.must('POST', '/api/v1/admin/apps', [200, 201], {
      as: admin,
      json: { name: appName },
    });
    const appId = (app.json.id ?? app.json.app?.id) as string;
    const botId = (app.json.user_id ?? app.json.app?.user_id) as string;
    for (const channelId of memberOf) {
      await this.must('PUT', `/api/v1/channels/${channelId}/members/${botId}`, [200, 201, 204], {
        as: admin,
        json: {},
      });
    }
    const tok = await this.must('POST', `/api/v1/admin/apps/${appId}/tokens`, [201], {
      as: admin,
      json: { scopes, channel_ids: null },
    });
    return {
      token: tok.json.token as string,
      appId,
      botId,
      connection: this.connectionString(tok.json.token as string),
    };
  }

  /** Creates an incoming webhook; returns its POST path on this server. */
  async createWebhook(admin: User, channelId: string, name: string): Promise<string> {
    const r = await this.must('POST', '/api/v1/admin/webhooks', [201], {
      as: admin,
      json: { channel_id: channelId, name },
    });
    const token = (r.json.url as string).split('/hooks/')[1] ?? '';
    return `/hooks/${token}`;
  }

  /** Connection string for the example programs, pointing at this server. */
  connectionString(token: string): string {
    const fp = new X509Certificate(this.ca).fingerprint256.replace(/:/g, '').toLowerCase();
    return `bubbles://127.0.0.1:${this.httpsPort}/?token=${token}&fp=sha256:${fp}`;
  }
}
