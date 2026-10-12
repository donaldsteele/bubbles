import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  BACKOFF_MAX_MS,
  backoffWindow,
  WsClient,
  type Hello,
  type WsEvent,
  type WsHandlers,
} from '../../src/lib/ws/client';
import { FakeWebSocket } from './fake-ws';

function hello(over: Partial<Hello> = {}): Hello {
  return {
    type: 'hello',
    instance_id: 'inst1',
    epoch: 1,
    seq_head: 10,
    user_id: 'u1',
    server_version: 'test',
    resume: 'ok',
    ...over,
  };
}

function setup(over: Partial<WsHandlers> = {}, random = () => 0.5) {
  const events: WsEvent[] = [];
  const handlers: WsHandlers = { onEvent: (e) => events.push(e), ...over };
  const client = new WsClient({
    handlers,
    WebSocketImpl: FakeWebSocket as unknown as typeof WebSocket,
    baseUrl: () => 'wss://h.test/api/v1/ws',
    random,
  });
  return { client, events };
}

const ev = (seq: number, type = 'message.created') => ({
  seq,
  type,
  channel_id: 'c1',
  ts: '2026-01-01T00:00:00.000Z',
  data: {},
});

describe('WsClient', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    FakeWebSocket.reset();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('builds the resume URL from bootstrap state', () => {
    const { client } = setup();
    client.setResumeState({ seq: 42, instanceId: 'inst1', epoch: 3 });
    expect(client.url()).toBe('wss://h.test/api/v1/ws?resume=42&instance=inst1&epoch=3');
    client.connect();
    expect(FakeWebSocket.last.url).toContain('resume=42');
  });

  it('ignores seq <= last_seq and applies ephemeral seq 0 every time', () => {
    const { client, events } = setup();
    client.setResumeState({ seq: 5, instanceId: 'inst1', epoch: 1 });
    client.connect();
    const ws = FakeWebSocket.last;
    ws.receive(hello());
    ws.receive(ev(5));
    ws.receive(ev(4));
    ws.receive(ev(6));
    ws.receive(ev(6));
    ws.receive(ev(0, 'typing'));
    ws.receive(ev(0, 'typing'));
    expect(events.map((e) => e.seq)).toEqual([6, 0, 0]);
    expect(client.lastSeq).toBe(6);
  });

  it('resync_required: reports it and ignores frames <= the new seq_head', () => {
    const onResync = vi.fn();
    const { client, events } = setup({ onResync });
    client.setResumeState({ seq: 3, instanceId: 'inst1', epoch: 1 });
    client.connect();
    const ws = FakeWebSocket.last;
    ws.receive(hello({ resume: 'resync_required', seq_head: 50, epoch: 2 }));
    expect(onResync).toHaveBeenCalledOnce();
    ws.receive(ev(50));
    ws.receive(ev(51));
    expect(events.map((e) => e.seq)).toEqual([51]);
    expect(client.epoch).toBe(2);
  });

  it('instance_changed: factory reset callback, no reconnect', () => {
    const onFactoryReset = vi.fn();
    const { client } = setup({ onFactoryReset });
    client.connect();
    FakeWebSocket.last.receive(hello({ resume: 'instance_changed' }));
    expect(onFactoryReset).toHaveBeenCalledOnce();
    vi.advanceTimersByTime(60_000);
    expect(FakeWebSocket.instances).toHaveLength(1);
  });

  it('backoff windows are bounded by 0.5 s .. 30 s with jitter', () => {
    expect(backoffWindow(0)).toEqual({ min: 250, max: 500 });
    expect(backoffWindow(1).max).toBe(1000);
    expect(backoffWindow(6).max).toBe(BACKOFF_MAX_MS);
    expect(backoffWindow(50).max).toBe(BACKOFF_MAX_MS);
  });

  it('reconnects with growing delay and resets it after a successful hello', () => {
    const { client } = setup({}, () => 1); // max of each window
    client.connect();
    FakeWebSocket.last.serverClose(1006);
    vi.advanceTimersByTime(499);
    expect(FakeWebSocket.instances).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(FakeWebSocket.instances).toHaveLength(2);

    FakeWebSocket.last.serverClose(1006); // attempt 1 -> 1000 ms
    vi.advanceTimersByTime(999);
    expect(FakeWebSocket.instances).toHaveLength(2);
    vi.advanceTimersByTime(1);
    expect(FakeWebSocket.instances).toHaveLength(3);

    FakeWebSocket.last.receive(hello()); // success resets the attempt counter
    FakeWebSocket.last.serverClose(1006);
    vi.advanceTimersByTime(500);
    expect(FakeWebSocket.instances).toHaveLength(4);
  });

  it('4008 reconnects immediately; 4009 does not reconnect', () => {
    const onSessionRevoked = vi.fn();
    const { client } = setup({ onSessionRevoked });
    client.connect();
    FakeWebSocket.last.serverClose(4008);
    vi.advanceTimersByTime(0);
    expect(FakeWebSocket.instances).toHaveLength(2);
    FakeWebSocket.last.serverClose(4009);
    vi.advanceTimersByTime(60_000);
    expect(FakeWebSocket.instances).toHaveLength(2);
    expect(onSessionRevoked).toHaveBeenCalledOnce();
  });

  it('4008 retries immediately only on the first attempt, then backs off', () => {
    const { client } = setup();
    client.connect();
    FakeWebSocket.last.serverClose(4008);
    vi.advanceTimersByTime(0);
    expect(FakeWebSocket.instances).toHaveLength(2);
    // a second slow_consumer close before any hello must not spin
    FakeWebSocket.last.serverClose(4008);
    vi.advanceTimersByTime(0);
    expect(FakeWebSocket.instances).toHaveLength(2);
    vi.advanceTimersByTime(1000);
    expect(FakeWebSocket.instances).toHaveLength(3);
  });

  it('a socket stuck in CONNECTING times out', () => {
    const { client } = setup();
    client.connect();
    vi.advanceTimersByTime(75_000);
    expect(FakeWebSocket.instances[0]?.closedWith).toBe(4000);
    vi.advanceTimersByTime(30_000);
    expect(FakeWebSocket.instances.length).toBeGreaterThan(1);
  });

  it('4010 reports a factory reset; 4011 reports content cleared and reconnects', () => {
    const onFactoryReset = vi.fn();
    const onContentCleared = vi.fn();
    const { client } = setup({ onFactoryReset, onContentCleared });
    client.connect();
    FakeWebSocket.last.serverClose(4011);
    expect(onContentCleared).toHaveBeenCalledOnce();
    vi.advanceTimersByTime(500);
    expect(FakeWebSocket.instances).toHaveLength(2);
    FakeWebSocket.last.serverClose(4010);
    expect(onFactoryReset).toHaveBeenCalledOnce();
    vi.advanceTimersByTime(60_000);
    expect(FakeWebSocket.instances).toHaveLength(2);
  });

  it('throttles typing to one per 3 s per channel and frames to 10 per second', () => {
    const { client } = setup();
    client.connect();
    FakeWebSocket.last.receive(hello());
    const ws = FakeWebSocket.last;
    expect(client.sendTyping('c1')).toBe(true);
    expect(client.sendTyping('c1')).toBe(false);
    expect(client.sendTyping('c2')).toBe(true);
    vi.advanceTimersByTime(3000);
    expect(client.sendTyping('c1')).toBe(true);

    vi.advanceTimersByTime(1000);
    let accepted = 0;
    for (let i = 0; i < 25; i++) if (client.sendPresence('active')) accepted++;
    expect(accepted).toBe(10);
    expect(ws.sent.length).toBe(13);
    vi.advanceTimersByTime(1000);
    expect(client.sendFocus(null, true)).toBe(true);
  });

  it('does not send when the socket is not open', () => {
    const { client } = setup();
    expect(client.sendPing()).toBe(false);
  });

  it('treats a silent socket as dead and reconnects', () => {
    const { client } = setup();
    client.connect();
    FakeWebSocket.last.receive(hello());
    vi.advanceTimersByTime(75_000);
    vi.advanceTimersByTime(500);
    expect(FakeWebSocket.instances).toHaveLength(2);
  });
});
