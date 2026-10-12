import { beforeEach, describe, expect, it, vi } from 'vitest';
import { resetLocalState, setHardNavigate, setPushUnsubscribe } from '../../src/lib/reset';
import { messages } from '../../src/lib/state/messages.svelte';
import { session } from '../../src/lib/state/session.svelte';
import type { Me, Message } from '../../src/lib/api/types';

const nav = vi.fn();

beforeEach(() => {
  localStorage.clear();
  nav.mockReset();
  setHardNavigate(nav);
  setPushUnsubscribe(null);
});

function seed() {
  localStorage.setItem('bubbles:instance', '{"instance_id":"a","epoch":1}');
  localStorage.setItem('bubbles:settings:u1', '{"theme":"light"}');
  localStorage.setItem('bubbles:draft:c1', 'hello');
  localStorage.setItem('bubbles:cache:c1', '[]');
  localStorage.setItem('other:key', 'keep');
}

describe('resetLocalState', () => {
  it('factory: clears every bubbles:* key, unsubscribes push and navigates to /', async () => {
    seed();
    const unsub = vi.fn();
    setPushUnsubscribe(unsub);
    session.setMe({ id: 'u1' } as Me);
    await resetLocalState('factory');
    expect(Object.keys(localStorage)).toEqual(['other:key']);
    expect(unsub).toHaveBeenCalledOnce();
    expect(nav).toHaveBeenCalledWith('/');
    expect(session.me).toBeNull();
  });

  it('factory: a failing push unsubscribe does not block the reset', async () => {
    seed();
    setPushUnsubscribe(() => Promise.reject(new Error('gone')));
    await resetLocalState('factory');
    expect(nav).toHaveBeenCalledWith('/');
  });

  it('content: clears drafts and message caches but keeps session and settings', async () => {
    seed();
    session.setMe({ id: 'u1' } as Me);
    messages.upsert({ id: '01A', channel_id: 'c1', thread_root_id: null } as Message);
    await resetLocalState('content');
    expect(localStorage.getItem('bubbles:draft:c1')).toBeNull();
    expect(localStorage.getItem('bubbles:cache:c1')).toBeNull();
    expect(localStorage.getItem('bubbles:settings:u1')).not.toBeNull();
    expect(localStorage.getItem('bubbles:instance')).not.toBeNull();
    expect(session.me).not.toBeNull();
    expect(messages.list('c1')).toEqual([]);
    expect(nav).not.toHaveBeenCalled();
  });
});
