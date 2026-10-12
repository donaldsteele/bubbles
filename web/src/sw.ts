// Service worker: app-shell precache, offline fallback, Web Push display.
// It never fetches on push and never caches API, hook, auth or status traffic.
import {
  cleanupOutdatedCaches,
  createHandlerBoundToURL,
  matchPrecache,
  precacheAndRoute,
} from 'workbox-precaching';
import { NavigationRoute, registerRoute, setCatchHandler } from 'workbox-routing';
import { CacheFirst, NetworkOnly } from 'workbox-strategies';
import {
  EMOJI_CACHE,
  OFFLINE_URL,
  classify,
  clickTarget,
  notificationFor,
  parsePush,
  type NavigateMessage,
} from './lib/pwa/sw-core';

// Minimal worker-scope typings: the DOM lib (used by the app) conflicts with "webworker".
interface ExtendableEventLike extends Event {
  waitUntil(p: Promise<unknown>): void;
}
interface WorkerClient {
  url: string;
  visibilityState?: string;
  focus(): Promise<unknown>;
  postMessage(m: unknown): void;
}
interface NotificationLike {
  data: { channel_id?: unknown; message_id?: unknown } | null;
  close(): void;
}
interface WorkerScope {
  __WB_MANIFEST: Array<string | { url: string; revision: string | null }>;
  location: Location;
  registration: {
    showNotification(title: string, options?: Record<string, unknown>): Promise<void>;
  };
  clients: {
    claim(): Promise<void>;
    matchAll(o: { type: 'window'; includeUncontrolled: boolean }): Promise<WorkerClient[]>;
    openWindow(url: string): Promise<unknown>;
  };
  addEventListener(type: string, listener: (e: never) => void): void;
}
const sw = self as unknown as WorkerScope;

// The build injects the precache list where it finds the literal `self.__WB_MANIFEST`.
precacheAndRoute((self as unknown as WorkerScope).__WB_MANIFEST);
cleanupOutdatedCaches();

registerRoute(
  ({ url, request }) =>
    classify(url, sw.location.origin, request.method, request.mode) === 'network-only',
  new NetworkOnly(),
);

registerRoute(
  ({ url, request }) =>
    classify(url, sw.location.origin, request.method, request.mode) === 'emoji-cache',
  new CacheFirst({ cacheName: EMOJI_CACHE }),
);

// Any other navigation gets the cached shell; the client router takes it from there.
registerRoute(
  new NavigationRoute(createHandlerBoundToURL('/index.html'), {
    denylist: [/^\/api(\/|$)/, /^\/hooks(\/|$)/, /^\/auth(\/|$)/],
  }),
);

setCatchHandler(async ({ request }) => {
  if (request.mode === 'navigate') {
    const cached = await matchPrecache(OFFLINE_URL);
    if (cached) return cached;
  }
  return Response.error();
});

// Take over open pages on first install; later updates wait for SKIP_WAITING.
sw.addEventListener('activate', (e: ExtendableEventLike) => e.waitUntil(sw.clients.claim()));

sw.addEventListener('message', (e: MessageEvent) => {
  const d: unknown = e.data;
  if (d && typeof d === 'object' && (d as { type?: unknown }).type === 'SKIP_WAITING') {
    (sw as unknown as { skipWaiting(): Promise<void> }).skipWaiting();
  }
});

sw.addEventListener('push', (e: ExtendableEventLike & { data: { text(): string } | null }) => {
  const plan = notificationFor(parsePush(e.data ? e.data.text() : null));
  e.waitUntil(sw.registration.showNotification(plan.title, plan.options));
});

sw.addEventListener(
  'notificationclick',
  (e: ExtendableEventLike & { notification: NotificationLike }) => {
    e.notification.close();
    const path = clickTarget(e.notification.data);
    e.waitUntil(
      (async () => {
        const all = await sw.clients.matchAll({ type: 'window', includeUncontrolled: true });
        const own = all.filter((c) => new URL(c.url).origin === sw.location.origin);
        const target = own.find((c) => c.visibilityState === 'visible') ?? own[0];
        if (target) {
          const msg: NavigateMessage = { type: 'navigate', path };
          await target.focus();
          target.postMessage(msg);
        } else {
          await sw.clients.openWindow(path);
        }
      })(),
    );
  },
);
