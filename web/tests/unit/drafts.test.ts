import { afterEach, describe, expect, it, vi } from 'vitest';
import { clearDraft, draftKey, loadDraft, saveDraft } from '../../src/lib/composer/drafts';
import { markerFor, wrapSelection } from '../../src/lib/composer/format';

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
});

describe('drafts', () => {
  it('saves per channel and thread, and clears', () => {
    saveDraft('C1', 'hello');
    saveDraft('C1', 'reply', 'R1');
    expect(localStorage.getItem('bubbles:draft:C1')).toBe('hello');
    expect(loadDraft('C1')).toBe('hello');
    expect(loadDraft('C1', 'R1')).toBe('reply');
    expect(draftKey('C1', 'R1')).toBe('bubbles:draft:C1:R1');
    clearDraft('C1');
    expect(loadDraft('C1')).toBe('');
  });

  it('removes the draft when the text is blank', () => {
    saveDraft('C1', 'x');
    saveDraft('C1', '   ');
    expect(localStorage.getItem('bubbles:draft:C1')).toBeNull();
  });

  it('survives storage exceptions', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('denied');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('quota');
    });
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => {
      throw new Error('denied');
    });
    expect(() => saveDraft('C1', 'x')).not.toThrow();
    expect(() => clearDraft('C1')).not.toThrow();
    expect(loadDraft('C1')).toBe('');
  });
});

describe('format shortcuts', () => {
  it('wraps a selection and toggles it off again', () => {
    const a = wrapSelection('say hi now', 4, 6, '**');
    expect(a.value).toBe('say **hi** now');
    expect(a.start).toBe(6);
    expect(a.end).toBe(8);
    const b = wrapSelection(a.value, 4, 10, '**');
    expect(b.value).toBe('say hi now');
  });

  it('inserts a pair around the caret with no selection', () => {
    const r = wrapSelection('ab', 1, 1, '`');
    expect(r).toEqual({ value: 'a``b', start: 2, end: 2 });
  });

  it('maps B/I/E', () => {
    expect([markerFor('b'), markerFor('I'), markerFor('e'), markerFor('x')]).toEqual([
      '**',
      '_',
      '`',
      null,
    ]);
  });
});
