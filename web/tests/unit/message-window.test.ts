import { beforeEach, describe, expect, it } from 'vitest';
import { msg } from './factory';
import { messages } from '../../src/lib/state/messages.svelte';
import { capWindow, mergeItems } from '../../src/lib/messages/paging';
import {
  MessageWindow,
  type PageFetcher,
  type PageQuery,
} from '../../src/lib/messages/view.svelte';

/** Fake server holding messages 1..total. */
function server(total: number): PageFetcher {
  return async (_ch, q: PageQuery) => {
    const all = Array.from({ length: total }, (_, i) => msg(i + 1));
    const idx = (id: string) => all.findIndex((m) => m.id === id);
    let from: number;
    let to: number;
    if (q.before) {
      to = idx(q.before);
      from = Math.max(0, to - q.limit);
    } else if (q.after) {
      from = idx(q.after) + 1;
      to = Math.min(total, from + q.limit);
    } else if (q.around) {
      from = Math.max(0, idx(q.around) - q.limit / 2);
      to = Math.min(total, from + q.limit);
    } else {
      to = total;
      from = Math.max(0, total - q.limit);
    }
    return { items: all.slice(from, to), has_more_before: from > 0, has_more_after: to < total };
  };
}

describe('paging helpers', () => {
  it('merges pages without duplicates, sorted', () => {
    const merged = mergeItems([msg(3), msg(4)], [msg(1), msg(2), msg(3)]);
    expect(merged.map((m) => m.id)).toEqual(['M00001', 'M00002', 'M00003', 'M00004']);
  });

  it('caps after loading older by dropping the newest', () => {
    const items = Array.from({ length: 10 }, (_, i) => msg(i + 1));
    const w = capWindow({ items, hasBefore: true, hasAfter: false }, 'older', 6);
    expect(w.items.map((m) => m.body)).toEqual(['m1', 'm2', 'm3', 'm4', 'm5', 'm6']);
    expect(w.hasAfter).toBe(true);
  });

  it('caps after loading newer by dropping the oldest', () => {
    const items = Array.from({ length: 10 }, (_, i) => msg(i + 1));
    const w = capWindow({ items, hasBefore: false, hasAfter: true }, 'newer', 6);
    expect(w.items[0]?.body).toBe('m5');
    expect(w.hasBefore).toBe(true);
  });
});

describe('MessageWindow DOM cap', () => {
  beforeEach(() => messages.clear());

  it('unloads the newest page when scrolling back through history, and reloads it', async () => {
    const win = new MessageWindow(server(1000), 120);
    await win.open('C1');
    expect(win.items).toHaveLength(50);
    expect(win.hasBefore).toBe(true);
    expect(win.hasAfter).toBe(false);

    for (let i = 0; i < 3; i++) await win.loadOlder();
    expect(win.items.length).toBeLessThanOrEqual(120);
    expect(win.hasAfter).toBe(true);
    expect(win.items[win.items.length - 1]?.id).not.toBe('M01000');

    // scroll back down: newer pages load again and the oldest ones unload
    const firstBefore = win.items[0]?.id as string;
    for (let i = 0; i < 4; i++) await win.loadNewer();
    expect(win.items.length).toBeLessThanOrEqual(120);
    expect((win.items[0]?.id as string) > firstBefore).toBe(true);
    expect(win.hasBefore).toBe(true);
  });

  it('hides live arrivals beyond the window and caps live growth at the bottom', async () => {
    const win = new MessageWindow(server(60), 55);
    await win.open('C1');
    for (let n = 61; n <= 70; n++) messages.upsert(msg(n));
    expect(win.items.length).toBe(60);
    win.enforceCap();
    expect(win.items.length).toBe(55);
    expect(win.hasBefore).toBe(true);
  });

  it('opens around an id', async () => {
    const win = new MessageWindow(server(500), 400);
    await win.open('C1', 'M00250');
    expect(win.hasBefore).toBe(true);
    expect(win.hasAfter).toBe(true);
    expect(win.items.some((m) => m.id === 'M00250')).toBe(true);
    expect(win.items.every((m) => m.id <= 'M00275')).toBe(true);
  });

  it('reports a failed load', async () => {
    const win = new MessageWindow(async () => {
      throw new Error('boom');
    });
    await win.open('C1');
    expect(win.failed).toBe(true);
    expect(win.loading).toBe('idle');
  });
});
