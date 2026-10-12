// Keyboard navigation: F6 region cycling and Alt+arrow channel switching.
import { navigate } from './router';

export const REGIONS = ['sidebar', 'messages', 'composer', 'drawer'] as const;
export type Region = (typeof REGIONS)[number];

/** Next/previous entry of a cyclic list. Returns the first/last when current is unknown. */
export function cycle<T>(list: readonly T[], current: T | undefined, dir: 1 | -1): T | undefined {
  if (list.length === 0) return undefined;
  const i = current === undefined ? -1 : list.indexOf(current);
  if (i === -1) return dir === 1 ? list[0] : list[list.length - 1];
  return list[(i + dir + list.length) % list.length];
}

/** Next/previous channel id in sidebar order, wrapping. */
export function adjacentChannel(
  ids: readonly string[],
  current: string | undefined,
  dir: 1 | -1,
): string | undefined {
  return cycle(ids, current, dir);
}

/** Next channel with unread messages in direction dir, wrapping; skips the current one. */
export function nextUnreadChannel(
  ids: readonly string[],
  current: string | undefined,
  dir: 1 | -1,
  hasUnread: (id: string) => boolean,
): string | undefined {
  const n = ids.length;
  if (n === 0) return undefined;
  const found = current === undefined ? -1 : ids.indexOf(current);
  const start = found === -1 ? (dir === 1 ? -1 : n) : found;
  for (let step = 1; step <= n; step++) {
    const id = ids[(((start + dir * step) % n) + n) % n] as string;
    if (id !== current && hasUnread(id)) return id;
  }
  return undefined;
}

function regionEl(name: Region): HTMLElement | null {
  return document.querySelector<HTMLElement>(`[data-region="${name}"]`);
}

export function availableRegions(): Region[] {
  return REGIONS.filter((r) => regionEl(r) !== null);
}

export function currentRegion(): Region | undefined {
  const active = document.activeElement;
  if (!active) return undefined;
  return REGIONS.find((r) => regionEl(r)?.contains(active));
}

export function focusRegion(name: Region): boolean {
  const el = regionEl(name);
  if (!el) return false;
  const direct = el.matches('textarea,input,[tabindex]');
  const target = direct
    ? el
    : (el.querySelector<HTMLElement>('[aria-current="page"]') ??
      el.querySelector<HTMLElement>(
        'a[href],button:not([disabled]),textarea,input,select,[tabindex]',
      ));
  (target ?? el).focus();
  return true;
}

export interface KeyboardDeps {
  /** Channel ids in sidebar order. */
  channelIds: () => string[];
  currentChannel: () => string | undefined;
  hasUnread: (id: string) => boolean;
}

/** True when keystrokes in this element belong to text entry. */
export function isTextEntry(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  const tag = target.tagName;
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT';
}

/** Installs the global shortcuts; returns an uninstaller. */
export function installKeyboard(deps: KeyboardDeps): () => void {
  const onKey = (e: KeyboardEvent) => {
    if (e.key === 'F6') {
      const next = cycle(availableRegions(), currentRegion(), e.shiftKey ? -1 : 1);
      if (next) {
        e.preventDefault();
        focusRegion(next);
      }
      return;
    }
    if (!e.altKey || e.ctrlKey || e.metaKey) return;
    if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return;
    if (isTextEntry(e.target)) return;
    const dir = e.key === 'ArrowDown' ? 1 : -1;
    const id = e.shiftKey
      ? nextUnreadChannel(deps.channelIds(), deps.currentChannel(), dir, deps.hasUnread)
      : adjacentChannel(deps.channelIds(), deps.currentChannel(), dir);
    if (id) {
      e.preventDefault();
      navigate(`/c/${id}`);
    }
  };
  document.addEventListener('keydown', onKey);
  return () => document.removeEventListener('keydown', onKey);
}
