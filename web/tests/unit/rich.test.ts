import { cleanup, render, screen } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import AlertCard from '../../src/components/alerts/AlertCard.svelte';
import {
  applySuggestion,
  findTrigger,
  listboxKey,
  suggestionsFor,
} from '../../src/lib/composer/autocomplete';
import { parseCommand } from '../../src/lib/composer/commands';
import { Outbox } from '../../src/lib/composer/outbox.svelte';
import type { EmojiData } from '../../src/lib/emoji/data';
import { searchEmoji } from '../../src/lib/emoji/data';
import { UploadQueue } from '../../src/lib/files/upload.svelte';
import { installKeyboard, isTextEntry } from '../../src/lib/keyboard';
import { meetsSeverity } from '../../src/lib/alerts/actions';
import { buildQuery, emptyFilters, sanitizeSnippet } from '../../src/lib/search/snippet';
import { fuzzyScore, rankEntries } from '../../src/lib/switcher';
import { msg } from './factory';

afterEach(() => {
  cleanup();
});

describe('snippet sanitizer', () => {
  it('keeps <mark> and strips every other tag and attribute', () => {
    const out = sanitizeSnippet(
      'a <mark>hit</mark> <img src=x onerror=alert(1)> <script>x</script><b onclick="y">b</b> <mark class="c">m</mark>',
    );
    expect(out).toContain('<mark>hit</mark>');
    expect(out).toContain('<mark>m</mark>');
    expect(out).not.toMatch(/<img|<script|<b|onerror|onclick|class=/);
  });

  it('builds the query grammar from filters', () => {
    expect(
      buildQuery('fix  ', {
        ...emptyFilters,
        channel: 'ops',
        from: 'ana',
        has: 'image',
        before: '2026-03-10',
        after: 'not-a-date',
      }),
    ).toBe('fix in:#ops from:@ana has:image before:2026-03-10');
  });
});

describe('command detection', () => {
  const known = ['topic', 'invite', 'me'];
  it('matches a known command and keeps its args', () => {
    expect(parseCommand('/topic New topic here', known)).toEqual({
      name: 'topic',
      args: 'New topic here',
    });
    expect(parseCommand('/ME waves', known)).toEqual({ name: 'me', args: 'waves' });
    expect(parseCommand('/topic', known)).toEqual({ name: 'topic', args: '' });
  });

  it('treats everything else as a message', () => {
    expect(parseCommand('/ not a command', known)).toBeNull();
    expect(parseCommand('/unknown x', known)).toBeNull();
    expect(parseCommand('see /topic please', known)).toBeNull();
    expect(parseCommand('//topic', known)).toBeNull();
  });
});

describe('autocomplete', () => {
  it('detects triggers at word starts only', () => {
    expect(findTrigger('hi @an', 6)).toMatchObject({ kind: 'user', query: 'an', start: 3 });
    expect(findTrigger('see #gen', 8)).toMatchObject({ kind: 'channel', query: 'gen' });
    expect(findTrigger('/to', 3)).toMatchObject({ kind: 'command', query: 'to' });
    expect(findTrigger('a /to', 5)).toBeNull();
    expect(findTrigger('mail@x', 6)).toBeNull();
    expect(findTrigger(':s', 2)).toBeNull();
    expect(findTrigger('x :smi', 6)).toMatchObject({ kind: 'emoji', query: 'smi' });
    expect(findTrigger('@an done', 8)).toBeNull();
  });

  it('moves with arrows (wrapping), accepts with Enter and closes with Escape', () => {
    expect(listboxKey('ArrowDown', 0, 3)).toEqual({ active: 1, action: 'move' });
    expect(listboxKey('ArrowDown', 2, 3)).toEqual({ active: 0, action: 'move' });
    expect(listboxKey('ArrowUp', 0, 3)).toEqual({ active: 2, action: 'move' });
    expect(listboxKey('Enter', 1, 3)).toEqual({ active: 1, action: 'accept' });
    expect(listboxKey('Escape', 1, 3)).toEqual({ active: 1, action: 'close' });
    expect(listboxKey('a', 1, 3).action).toBe('none');
    expect(listboxKey('Enter', 0, 0).action).toBe('none');
  });

  it('suggests users, commands and emoji and applies the choice', () => {
    const data: EmojiData = {
      groups: ['smileys-emotion'],
      items: [
        { e: '😄', n: 'grinning face with smiling eyes', g: 0, s: ['smile'], t: ['happy'] },
        { e: '😀', n: 'grinning face', g: 0, s: ['grinning'], t: [] },
      ],
    };
    const ctx = {
      users: [
        { id: 'U1', username: 'ana', display_name: 'Ana', deactivated: false },
        { id: 'U2', username: 'bob', display_name: 'Bob', deactivated: false },
      ] as never,
      channels: [],
      commands: [{ name: 'topic', description: 'Set topic', usage: '/topic text' }],
      emoji: data,
    };
    const user = findTrigger('hi @a', 5);
    const us = suggestionsFor(user!, ctx);
    expect(us.map((s) => s.label)).toEqual(['@ana']);
    expect(applySuggestion('hi @a', user!, us[0]!.insert)).toEqual({
      value: 'hi @ana ',
      caret: 8,
    });
    const cmd = suggestionsFor(findTrigger('/t', 2)!, ctx);
    expect(cmd[0]?.insert).toBe('/topic ');
    const em = suggestionsFor(findTrigger(':smi', 4)!, ctx);
    expect(em[0]?.insert).toBe('😄');
    expect(searchEmoji(data, 'happy')[0]?.e).toBe('😄');
  });
});

class FakeXhr {
  static all: FakeXhr[] = [];
  upload: { onprogress: ((e: unknown) => void) | null } = { onprogress: null };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  ontimeout: (() => void) | null = null;
  withCredentials = false;
  responseType = '';
  status = 0;
  responseText = '';
  aborted = false;
  sent = false;
  constructor() {
    FakeXhr.all.push(this);
  }
  open() {}
  send() {
    this.sent = true;
  }
  abort() {
    this.aborted = true;
  }
  finish(status: number, body: unknown) {
    this.status = status;
    this.responseText = JSON.stringify(body);
    this.onload?.();
  }
}

const file = (name: string, size: number) => new File([new Uint8Array(size)], name);

describe('UploadQueue', () => {
  const make = (max = 1000) => {
    FakeXhr.all = [];
    return new UploadQueue(
      () => max,
      () => new FakeXhr() as unknown as XMLHttpRequest,
    );
  };

  it('rejects files over the limit without a request', () => {
    const q = make(10);
    q.add([file('big.bin', 11)]);
    expect(q.items[0]?.status).toBe('rejected');
    expect(q.items[0]?.error).toBe('tooLarge');
    expect(FakeXhr.all).toHaveLength(0);
  });

  it('cancel aborts the in-flight XHR and removes the item', () => {
    const q = make();
    q.add([file('a.txt', 5)]);
    const x = FakeXhr.all[0]!;
    expect(x.sent).toBe(true);
    expect(q.busy).toBe(true);
    q.cancel(q.items[0]!.id);
    expect(x.aborted).toBe(true);
    expect(q.items).toHaveLength(0);
    expect(q.busy).toBe(false);
  });

  it('reports progress, finishes, and retries a failure', () => {
    const q = make();
    q.add([file('a.txt', 5)]);
    const x = FakeXhr.all[0]!;
    x.upload.onprogress?.({ lengthComputable: true, loaded: 1, total: 4 });
    expect(q.items[0]?.progress).toBe(0.25);
    x.finish(500, {});
    expect(q.items[0]?.status).toBe('failed');
    q.retry(q.items[0]!.id);
    const y = FakeXhr.all[1]!;
    expect(y.sent).toBe(true);
    y.finish(201, { id: 'F1', category: 'file', meta_stripped: true });
    expect(q.items[0]?.status).toBe('done');
    expect(q.done.map((f) => f.id)).toEqual(['F1']);
  });

  it('runs at most two uploads at once', () => {
    const q = make();
    q.add([file('1', 1), file('2', 1), file('3', 1)]);
    expect(FakeXhr.all).toHaveLength(2);
    FakeXhr.all[0]!.finish(201, { id: 'F1', category: 'file' });
    expect(FakeXhr.all).toHaveLength(3);
  });
});

describe('AlertCard', () => {
  const alertMsg = (severity: 'info' | 'warning' | 'critical', state = 'open') =>
    msg(1, {
      kind: 'alert',
      alert: {
        severity,
        state,
        title: 'Gateway down',
        fields: [{ name: 'IP', value: '10.0.0.1', short: true }],
        link: 'https://example.com/x',
        dedupe_key: 'k',
        occurrences: 12,
        first_seen_at: '2026-01-01T12:00:00.000Z',
        last_seen_at: '2026-01-01T12:00:00.000Z',
        acked_by: null,
        acked_at: null,
        resolved_by: null,
        resolved_at: null,
      } as never,
    });

  it('shows the severity word, shape, fields, counts and actions', () => {
    const { container } = render(AlertCard, { message: alertMsg('critical') });
    expect(screen.getByText('Critical')).toBeTruthy();
    expect(container.querySelector('svg[data-shape="diamond"]')).toBeTruthy();
    expect(screen.getByText('Gateway down')).toBeTruthy();
    expect(screen.getByText('10.0.0.1')).toBeTruthy();
    expect(screen.getByText(/×12/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Acknowledge' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Resolve' })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'View thread' })).toBeTruthy();
  });

  it('uses a triangle for warning and the OK entry once resolved', () => {
    const w = render(AlertCard, { message: alertMsg('warning') });
    expect(w.container.querySelector('svg[data-shape="triangle"]')).toBeTruthy();
    expect(screen.getByText('Warning')).toBeTruthy();
    cleanup();
    render(AlertCard, { message: alertMsg('critical', 'resolved') });
    expect(screen.getByText('OK')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Resolve' })).toBeNull();
  });

  it('filters by minimum severity', () => {
    expect(meetsSeverity('info', 'warning')).toBe(false);
    expect(meetsSeverity('critical', 'warning')).toBe(true);
    expect(meetsSeverity('info', 'all')).toBe(true);
  });
});

describe('Alt+arrow shortcuts', () => {
  const press = (target: Element) => {
    const e = new KeyboardEvent('keydown', {
      key: 'ArrowDown',
      altKey: true,
      bubbles: true,
      cancelable: true,
    });
    const prevented = !target.dispatchEvent(e);
    return prevented;
  };

  it('are ignored while typing in a text field', () => {
    const go = vi.spyOn(history, 'pushState');
    const off = installKeyboard({
      channelIds: () => ['a', 'b'],
      currentChannel: () => 'a',
      hasUnread: () => false,
    });
    const ta = document.createElement('textarea');
    const input = document.createElement('input');
    const div = document.createElement('div');
    document.body.append(ta, input, div);
    expect(isTextEntry(ta)).toBe(true);
    expect(isTextEntry(input)).toBe(true);
    expect(isTextEntry(div)).toBe(false);
    expect(press(ta)).toBe(false);
    expect(press(input)).toBe(false);
    expect(go).not.toHaveBeenCalled();
    expect(press(div)).toBe(true);
    off();
    ta.remove();
    input.remove();
    div.remove();
    go.mockRestore();
  });
});

describe('outbox reconciliation', () => {
  it('drops pending and failed rows matched by an incoming own message', () => {
    const box = new Outbox(() => Promise.reject(new Error('x')));
    const a = box.enqueue('C1', 'hello');
    const b = box.enqueue('C1', 'hello');
    b.status = 'failed';
    const other = box.enqueue('C1', 'different');
    const thread = box.enqueue('C1', 'hello', 'ROOT');
    const sending = box.enqueue('C1', 'hello');
    sending.status = 'sending';
    box.reconcile({ channel_id: 'C1', thread_root_id: null, body: 'hello' });
    const left = box.items.map((i) => i.nonce);
    expect(left).not.toContain(a.nonce);
    expect(left).not.toContain(b.nonce);
    expect(left).toContain(other.nonce);
    expect(left).toContain(thread.nonce);
    expect(left).toContain(sending.nonce);
  });
});

describe('quick switcher matching', () => {
  it('ranks prefix matches first and supports subsequences', () => {
    expect(fuzzyScore('gen', 'general')).toBe(0);
    expect(fuzzyScore('eral', 'general')).toBeGreaterThan(0);
    expect(fuzzyScore('gnl', 'general')).not.toBeNull();
    expect(fuzzyScore('xyz', 'general')).toBeNull();
    const out = rankEntries('ops', [
      { id: '1', label: 'devops', dm: false },
      { id: '2', label: 'ops', dm: false },
      { id: '3', label: 'random', dm: false },
    ]);
    expect(out.map((e) => e.id)).toEqual(['2', '1']);
  });
});
