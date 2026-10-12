// resetLocalState implements pipeline step 21: the client's reaction to a
// server factory reset (4010 / instance_changed) or content clear (4011 /
// epoch change).
import { resetNotice } from './state/resetNotice.svelte';
import { PREFIX, KEYS, removeByPrefix } from './storage';

export type ResetLevel = 'factory' | 'content';

type Handler = (level: ResetLevel) => void;
const handlers = new Set<Handler>();
let pushUnsubscribe: (() => Promise<void> | void) | null = null;
let adminReset = false;
let deferredNavigation = false;
/**
 * Resolves once the server answers something other than 503 or a network
 * error. Other devices learn about a reset from the socket close, which comes
 * before the server is out of maintenance mode; loading the page then would
 * show the raw 503 where no service worker can serve the app shell.
 */
async function whenServing(): Promise<void> {
  for (let i = 0; i < 120; i++) {
    try {
      const res = await fetch('/api/v1/status', { cache: 'no-store' });
      if (res.status !== 503) return;
    } catch {
      // the server is restarting its listeners
    }
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
}

let hardNavigate: (path: string) => void = (path) => {
  void whenServing().then(() => location.assign(path));
};

/** In-memory state modules register here to drop their data on reset. */
export function onLocalReset(fn: Handler): () => void {
  handlers.add(fn);
  return () => handlers.delete(fn);
}

/** The push module registers its unsubscribe hook here. */
export function setPushUnsubscribe(fn: (() => Promise<void> | void) | null): void {
  pushUnsubscribe = fn;
}

/**
 * The admin who runs a reset from this tab holds the navigation and the notice:
 * the 4010 close arrives before the answer that carries the new admin password,
 * and the page must stay until it is shown. Releasing runs a deferred
 * navigation unless `navigate` is false.
 */
export function holdResetNavigation(hold: boolean, navigate = true): void {
  adminReset = hold;
  if (!hold) {
    const pending = deferredNavigation;
    deferredNavigation = false;
    if (pending && navigate) hardNavigate('/');
  }
}

/** Test seam for the hard navigation. */
export function setHardNavigate(fn: (path: string) => void): void {
  hardNavigate = fn;
}

async function deleteDatabases(match: (name: string) => boolean): Promise<void> {
  try {
    if (typeof indexedDB === 'undefined' || typeof indexedDB.databases !== 'function') return;
    for (const db of await indexedDB.databases()) {
      if (db.name && match(db.name)) indexedDB.deleteDatabase(db.name);
    }
  } catch {
    // ignore
  }
}

export async function resetLocalState(level: ResetLevel): Promise<void> {
  if (!adminReset) resetNotice.show(level);
  if (level === 'factory') {
    removeByPrefix([PREFIX]);
    removeByPrefix([PREFIX], 'session');
    await deleteDatabases((n) => n.startsWith('bubbles'));
    try {
      await pushUnsubscribe?.();
    } catch {
      // the subscription may already be gone
    }
    try {
      if (typeof caches !== 'undefined') {
        // Runtime caches go; the service worker's app-shell precache stays.
        for (const k of await caches.keys()) {
          if (k.startsWith('bubbles-') && !/precache/i.test(k)) await caches.delete(k);
        }
      }
    } catch {
      // ignore
    }
  } else {
    // Session and preferences stay; drafts and message caches go.
    removeByPrefix([KEYS.draftPrefix, KEYS.cachePrefix]);
    await deleteDatabases((n) => n.startsWith('bubbles-cache'));
  }
  for (const h of handlers) h(level);
  if (level === 'factory') {
    if (adminReset) deferredNavigation = true;
    else hardNavigate('/');
  }
}
