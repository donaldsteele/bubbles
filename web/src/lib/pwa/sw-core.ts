// Pure decisions shared by the service worker and its unit tests. Nothing here
// touches the DOM, the network or any service worker API.

/** Runtime caches are all named with this prefix; reset.ts deletes only these. */
export const CACHE_PREFIX = 'bubbles-';
export const EMOJI_CACHE = `${CACHE_PREFIX}emoji`;
export const OFFLINE_URL = '/offline.html';
export const ICON_URL = '/icons/icon-192.png';

export type Strategy = 'network-only' | 'emoji-cache' | 'navigation' | 'default';

const NETWORK_ONLY = [/^\/api(\/|$)/, /^\/hooks(\/|$)/, /^\/auth(\/|$)/, /^\/setup/];

/**
 * Picks how the worker answers a request. 'default' leaves it to the precache
 * route (or the browser). Cross-origin and non-GET requests are never cached,
 * and the WebSocket upgrade never reaches a worker, but ws(s) URLs are still
 * classified network-only so the rule is explicit.
 */
export function classify(url: URL, origin: string, method: string, mode: string): Strategy {
  if (url.protocol === 'ws:' || url.protocol === 'wss:') return 'network-only';
  if (url.origin !== origin) return 'network-only';
  if (NETWORK_ONLY.some((re) => re.test(url.pathname))) return 'network-only';
  if (method !== 'GET') return 'network-only';
  if (mode === 'navigate') return 'navigation';
  if (url.pathname.startsWith('/emoji/')) return 'emoji-cache';
  return 'default';
}

/** Payload of a Web Push message (docs: {title, channel_id, message_id, body?}). */
export interface PushPayload {
  title: string;
  channel_id?: string;
  message_id?: string;
  body?: string;
}

export interface NotificationPlan {
  title: string;
  options: {
    body?: string;
    tag?: string;
    icon: string;
    badge: string;
    data: { channel_id?: string; message_id?: string };
  };
}

function str(v: unknown): string | undefined {
  return typeof v === 'string' && v !== '' ? v : undefined;
}

/** Parses push text; anything unusable yields a generic payload, never an error. */
export function parsePush(text: string | null): PushPayload {
  try {
    const v: unknown = text ? JSON.parse(text) : null;
    if (v && typeof v === 'object') {
      const o = v as Record<string, unknown>;
      return {
        title: str(o['title']) ?? 'bubbles',
        channel_id: str(o['channel_id']),
        message_id: str(o['message_id']),
        body: str(o['body']),
      };
    }
  } catch {
    // fall through
  }
  return { title: 'bubbles' };
}

/** Maps a payload to showNotification arguments; the tag groups by channel. */
export function notificationFor(p: PushPayload): NotificationPlan {
  const options: NotificationPlan['options'] = {
    icon: ICON_URL,
    badge: ICON_URL,
    data: {},
  };
  if (p.body) options.body = p.body;
  if (p.channel_id) {
    options.tag = p.channel_id;
    options.data.channel_id = p.channel_id;
  }
  if (p.message_id) options.data.message_id = p.message_id;
  return { title: p.title, options };
}

/** Route to open for a clicked notification. */
export function clickTarget(data: { channel_id?: unknown; message_id?: unknown } | null): string {
  const channel = str(data?.channel_id);
  if (!channel) return '/';
  const message = str(data?.message_id);
  const base = `/c/${encodeURIComponent(channel)}`;
  return message ? `${base}?around=${encodeURIComponent(message)}` : base;
}

/** Message the worker posts to a page to make it navigate. */
export interface NavigateMessage {
  type: 'navigate';
  path: string;
}

export function isNavigateMessage(v: unknown): v is NavigateMessage {
  if (!v || typeof v !== 'object') return false;
  const o = v as Record<string, unknown>;
  return o['type'] === 'navigate' && typeof o['path'] === 'string';
}
