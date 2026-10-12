import { describe, expect, it, vi } from 'vitest';
import { ApiError } from '../../src/lib/api/client';
import type { Message } from '../../src/lib/api/types';
import { MAX_NETWORK_ATTEMPTS, newNonce, Outbox } from '../../src/lib/composer/outbox.svelte';

const stored = { id: 'M1' } as Message;

describe('newNonce', () => {
  it('is 26 Crockford chars, time ordered, and random', () => {
    const a = newNonce(1_000_000);
    const b = newNonce(2_000_000);
    expect(a).toMatch(/^[0-9A-HJKMNP-TV-Z]{26}$/);
    expect(a.slice(0, 10) < b.slice(0, 10)).toBe(true);
    expect(newNonce(1_000_000)).not.toBe(a);
  });
});

describe('Outbox', () => {
  it('sends once, removes the item and reports the stored message', async () => {
    const send = vi.fn().mockResolvedValue(stored);
    const onSent = vi.fn();
    const box = new Outbox(send, onSent);
    const item = box.enqueue('C1', 'hi');
    await box.deliver(item.nonce);
    expect(send).toHaveBeenCalledOnce();
    expect(box.items).toHaveLength(0);
    expect(onSent).toHaveBeenCalledWith(stored, expect.objectContaining({ body: 'hi' }));
  });

  it('keeps network failures pending and resends with the same nonce on flush', async () => {
    const send = vi
      .fn()
      .mockRejectedValueOnce(new ApiError(0, 'network', 'down'))
      .mockResolvedValueOnce(stored);
    const box = new Outbox(send);
    const item = box.enqueue('C1', 'hi', null, 'NONCE1');
    await box.deliver(item.nonce);
    expect(box.items[0]?.status).toBe('pending');
    await box.flush();
    expect(send).toHaveBeenCalledTimes(2);
    expect(send.mock.calls[0]?.[0].nonce).toBe('NONCE1');
    expect(send.mock.calls[1]?.[0].nonce).toBe('NONCE1');
    expect(box.items).toHaveLength(0);
  });

  it('marks a server error failed; retry reuses the nonce; remove deletes', async () => {
    const send = vi
      .fn()
      .mockRejectedValueOnce(new ApiError(400, 'unknown', 'bad'))
      .mockResolvedValueOnce(stored);
    const box = new Outbox(send);
    const item = box.enqueue('C1', 'hi', 'ROOT', 'N2');
    await box.deliver(item.nonce);
    expect(box.items[0]?.status).toBe('failed');
    await box.flush(); // failed items are not resent automatically
    expect(send).toHaveBeenCalledOnce();
    await box.retry('N2');
    expect(send).toHaveBeenCalledTimes(2);
    expect(send.mock.calls[1]?.[0]).toMatchObject({ nonce: 'N2', threadRootId: 'ROOT' });

    const other = box.enqueue('C1', 'x');
    box.remove(other.nonce);
    expect(box.items).toHaveLength(0);
  });

  it('gives up after repeated network failures', async () => {
    const send = vi.fn().mockRejectedValue(new ApiError(0, 'network', 'down'));
    const box = new Outbox(send);
    const item = box.enqueue('C1', 'hi');
    for (let i = 0; i < MAX_NETWORK_ATTEMPTS; i++) await box.deliver(item.nonce);
    expect(box.items[0]?.status).toBe('failed');
  });

  it('scopes items by channel and thread', () => {
    const box = new Outbox(vi.fn());
    box.enqueue('C1', 'a');
    box.enqueue('C1', 'b', 'R1');
    box.enqueue('C2', 'c');
    expect(box.forScope('C1')).toHaveLength(1);
    expect(box.forScope('C1', 'R1')).toHaveLength(1);
  });
});
