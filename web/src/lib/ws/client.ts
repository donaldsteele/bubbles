// WebSocket client for GET /api/v1/ws (docs/protocol.md). Transport only: it
// owns reconnect, resume bookkeeping and client-frame throttles, and reports
// everything else through callbacks.

export type ResumeResult = 'ok' | 'resync_required' | 'instance_changed';

export interface Hello {
  type: 'hello';
  instance_id: string;
  epoch: number;
  seq_head: number;
  user_id: string;
  server_version: string;
  resume: ResumeResult;
}

export interface WsEvent {
  seq: number;
  type: string;
  channel_id?: string;
  ts: string;
  data: Record<string, unknown>;
}

export type ConnState = 'idle' | 'connecting' | 'open' | 'backoff' | 'stopped';

export interface WsHandlers {
  onEvent(ev: WsEvent): void;
  onHello?(h: Hello): void;
  /** hello said resync_required: drop caches and re-fetch /bootstrap. */
  onResync?(h: Hello): void;
  /** hello said instance_changed or close code 4010. */
  onFactoryReset?(): void;
  /** close code 4011: admin cleared content. */
  onContentCleared?(): void;
  /** close code 4009: do not reconnect, show sign-in. */
  onSessionRevoked?(): void;
  onState?(s: ConnState): void;
}

export interface WsOptions {
  handlers: WsHandlers;
  /** WebSocket constructor; tests inject a fake. */
  WebSocketImpl?: typeof WebSocket;
  baseUrl?: () => string;
  random?: () => number;
  now?: () => number;
}

export const BACKOFF_MIN_MS = 500;
export const BACKOFF_MAX_MS = 30_000;
export const TYPING_INTERVAL_MS = 3_000;
export const MAX_FRAMES_PER_SEC = 10;
export const PING_INTERVAL_MS = 30_000;
export const SILENCE_LIMIT_MS = 75_000;

/** Delay window for attempt n (0-based): equal jitter over min(cap, 500 * 2^n). */
export function backoffWindow(attempt: number): { min: number; max: number } {
  const base = Math.min(BACKOFF_MAX_MS, BACKOFF_MIN_MS * 2 ** Math.min(attempt, 20));
  return { min: base / 2, max: base };
}

function defaultUrl(): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${location.host}/api/v1/ws`;
}

export class WsClient {
  lastSeq = 0;
  instanceId = '';
  epoch = 0;
  state: ConnState = 'idle';

  #h: WsHandlers;
  #Ctor: typeof WebSocket;
  #baseUrl: () => string;
  #random: () => number;
  #now: () => number;
  #ws: WebSocket | null = null;
  #attempt = 0;
  #retry: ReturnType<typeof setTimeout> | null = null;
  #ping: ReturnType<typeof setInterval> | null = null;
  #silence: ReturnType<typeof setTimeout> | null = null;
  #typingAt = new Map<string, number>();
  #sent: number[] = [];

  constructor(opts: WsOptions) {
    this.#h = opts.handlers;
    this.#Ctor = opts.WebSocketImpl ?? WebSocket;
    this.#baseUrl = opts.baseUrl ?? defaultUrl;
    this.#random = opts.random ?? Math.random;
    this.#now = opts.now ?? Date.now;
  }

  /** Sets resume state from /bootstrap. Never moves lastSeq backwards. */
  setResumeState(s: { seq: number; instanceId: string; epoch: number }): void {
    this.lastSeq = Math.max(this.lastSeq, s.seq);
    this.instanceId = s.instanceId;
    this.epoch = s.epoch;
  }

  /** Forgets resume state (after a local reset). */
  clearResumeState(): void {
    this.lastSeq = 0;
    this.instanceId = '';
    this.epoch = 0;
  }

  url(): string {
    const q = new URLSearchParams();
    if (this.lastSeq > 0) q.set('resume', String(this.lastSeq));
    if (this.instanceId) q.set('instance', this.instanceId);
    if (this.epoch > 0) q.set('epoch', String(this.epoch));
    const qs = q.toString();
    return qs ? `${this.#baseUrl()}?${qs}` : this.#baseUrl();
  }

  connect(): void {
    if (this.state === 'connecting' || this.state === 'open') return;
    this.#clearRetry();
    this.#setState('connecting');
    const ws = new this.#Ctor(this.url());
    this.#ws = ws;
    ws.onmessage = (m) => {
      if (this.#ws !== ws) return;
      this.#armSilence();
      if (typeof m.data === 'string') this.handleFrame(m.data);
    };
    ws.onclose = (c) => {
      if (this.#ws !== ws) return;
      this.#ws = null;
      this.#teardownTimers();
      this.handleClose(c.code);
    };
    ws.onerror = () => {
      // onclose follows and drives the reconnect
    };
    this.#armSilence(); // a socket stuck in CONNECTING also times out
  }

  /** Stops for good (logout, revoked session). */
  stop(): void {
    this.#clearRetry();
    this.#teardownTimers();
    const ws = this.#ws;
    this.#ws = null;
    this.#setState('stopped');
    ws?.close(1000);
  }

  /** Reconnect now, skipping any backoff wait (the "retry now" button). */
  reconnectNow(): void {
    this.#clearRetry();
    const ws = this.#ws;
    this.#ws = null;
    this.#teardownTimers();
    this.#setState('idle');
    ws?.close(1000);
    this.connect();
  }

  handleFrame(raw: string): void {
    let f: Record<string, unknown>;
    try {
      f = JSON.parse(raw) as Record<string, unknown>;
    } catch {
      return;
    }
    if (f === null || typeof f !== 'object') return;
    if (f.type === 'pong') return;
    if (f.type === 'hello') {
      this.#onHello(f as unknown as Hello);
      return;
    }
    const seq = typeof f.seq === 'number' ? f.seq : -1;
    if (seq < 0 || typeof f.type !== 'string') return;
    if (seq > 0) {
      if (seq <= this.lastSeq) return; // replay overlap or duplicate
      this.lastSeq = seq;
    }
    this.#h.onEvent({ ...(f as unknown as WsEvent), data: (f.data as WsEvent['data']) ?? {} });
  }

  handleClose(code: number): void {
    switch (code) {
      case 4009:
        this.#setState('stopped');
        this.#h.onSessionRevoked?.();
        return;
      case 4010:
        this.#setState('stopped');
        this.#h.onFactoryReset?.();
        return;
      case 4011:
        this.#h.onContentCleared?.();
        this.#scheduleReconnect(true);
        return;
      case 4008:
        // Resume right away once; repeated slow_consumer closes back off.
        this.#scheduleReconnect(false, this.#attempt === 0);
        return;
      default:
        // 1000/1001/1006/1008/1009/1013 and anything else: back off and resume.
        this.#scheduleReconnect(false);
    }
  }

  sendTyping(channelId: string): boolean {
    const now = this.#now();
    const last = this.#typingAt.get(channelId);
    if (last !== undefined && now - last < TYPING_INTERVAL_MS) return false;
    if (!this.#send({ type: 'typing', channel_id: channelId })) return false;
    this.#typingAt.set(channelId, now);
    return true;
  }

  sendPresence(state: 'active' | 'idle'): boolean {
    return this.#send({ type: 'presence', state });
  }

  sendFocus(channelId: string | null, visible: boolean): boolean {
    return this.#send({
      type: 'focus',
      channel_id: channelId,
      visible,
      ts: new Date(this.#now()).toISOString(),
    });
  }

  sendPing(): boolean {
    return this.#send({ type: 'ping' });
  }

  #send(frame: Record<string, unknown>): boolean {
    const ws = this.#ws;
    if (!ws || this.state !== 'open' || ws.readyState !== 1) return false;
    const now = this.#now();
    this.#sent = this.#sent.filter((t) => now - t < 1000);
    if (this.#sent.length >= MAX_FRAMES_PER_SEC) return false;
    this.#sent.push(now);
    ws.send(JSON.stringify(frame));
    return true;
  }

  #onHello(h: Hello): void {
    this.#attempt = 0;
    this.#setState('open');
    this.#armSilence();
    if (this.#ping === null) {
      this.#ping = setInterval(() => this.sendPing(), PING_INTERVAL_MS);
    }
    this.#h.onHello?.(h);
    if (h.resume === 'instance_changed') {
      this.#ws?.close(1000);
      this.#ws = null;
      this.#teardownTimers();
      this.#setState('stopped');
      this.#h.onFactoryReset?.();
      return;
    }
    this.instanceId = h.instance_id;
    this.epoch = h.epoch;
    if (h.resume === 'resync_required') {
      // Frames at or below the head belong to state the re-fetch will cover.
      this.lastSeq = h.seq_head;
      this.#h.onResync?.(h);
    }
  }

  #scheduleReconnect(afterContentClear: boolean, immediate = false): void {
    if (this.state === 'stopped') return;
    if (afterContentClear) {
      // The server will answer resync_required for the old epoch.
      this.lastSeq = 0;
    }
    const w = backoffWindow(this.#attempt);
    const delay = immediate ? 0 : w.min + this.#random() * (w.max - w.min);
    this.#attempt += 1;
    this.#setState('backoff');
    this.#retry = setTimeout(() => {
      this.#retry = null;
      this.connect();
    }, delay);
  }

  #armSilence(): void {
    if (this.#silence) clearTimeout(this.#silence);
    this.#silence = setTimeout(() => {
      // No frame (not even a pong) for too long: treat the link as dead.
      const ws = this.#ws;
      if (!ws) return;
      this.#ws = null;
      this.#teardownTimers();
      ws.close(4000);
      this.handleClose(1006);
    }, SILENCE_LIMIT_MS);
  }

  #teardownTimers(): void {
    if (this.#silence) clearTimeout(this.#silence);
    if (this.#ping) clearInterval(this.#ping);
    this.#silence = null;
    this.#ping = null;
  }

  #clearRetry(): void {
    if (this.#retry) clearTimeout(this.#retry);
    this.#retry = null;
  }

  #setState(s: ConnState): void {
    if (this.state === s) return;
    this.state = s;
    this.#h.onState?.(s);
  }
}
