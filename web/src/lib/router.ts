// Minimal history router: route table, guards, link interception and focus
// management. No framework dependency; components subscribe to changes.

export type RouteName =
  | 'home'
  | 'setup'
  | 'login'
  | 'invite'
  | 'reset'
  | 'channel'
  | 'thread'
  | 'admin'
  | 'settings'
  | 'alerts'
  | 'notfound';

export interface Route {
  name: RouteName;
  params: Record<string, string>;
  /** Path after the matched prefix for /admin/* and /settings/*. */
  rest: string;
  path: string;
}

const PATTERNS: { name: RouteName; re: RegExp; keys: string[] }[] = [
  { name: 'home', re: /^\/$/, keys: [] },
  { name: 'setup', re: /^\/setup$/, keys: [] },
  { name: 'login', re: /^\/login$/, keys: [] },
  { name: 'invite', re: /^\/invite\/([^/]+)$/, keys: ['code'] },
  { name: 'reset', re: /^\/reset\/([^/]+)$/, keys: ['code'] },
  { name: 'channel', re: /^\/c\/([^/]+)$/, keys: ['channelId'] },
  { name: 'thread', re: /^\/c\/([^/]+)\/t\/([^/]+)$/, keys: ['channelId', 'messageId'] },
  { name: 'alerts', re: /^\/alerts$/, keys: [] },
  { name: 'admin', re: /^\/admin(?:\/(.*))?$/, keys: ['rest'] },
  { name: 'settings', re: /^\/settings(?:\/(.*))?$/, keys: ['rest'] },
];

const ULID_RE = /^[0-9A-HJKMNP-TV-Z]{26}$/;

/** Reads the message id of a "?around=<message_id>" query (null when absent or malformed). */
export function parseAround(search: string): string | null {
  const v = new URLSearchParams(search).get('around');
  return v !== null && ULID_RE.test(v) ? v : null;
}

export function matchRoute(rawPath: string): Route {
  const path = rawPath.length > 1 ? rawPath.replace(/\/+$/, '') : rawPath;
  for (const p of PATTERNS) {
    const m = p.re.exec(path);
    if (!m) continue;
    const params: Record<string, string> = {};
    p.keys.forEach((k, i) => {
      params[k] = decodeURIComponent(m[i + 1] ?? '');
    });
    const rest = params.rest ?? '';
    delete params.rest;
    return { name: p.name, params, rest, path };
  }
  return { name: 'notfound', params: {}, rest: '', path };
}

export interface GuardContext {
  authenticated: boolean;
  needsSetup: boolean | null;
  isAdmin: boolean;
}

const PUBLIC: RouteName[] = ['setup', 'login', 'invite', 'reset'];
const ADMIN_ONLY: RouteName[] = ['admin'];

/** Returns a redirect target for a route, or null when it may be shown. */
export function guard(route: Route, ctx: GuardContext): string | null {
  if (ctx.needsSetup === true) return route.name === 'setup' ? null : '/setup';
  if (route.name === 'setup') return ctx.authenticated ? '/' : '/login';
  if (route.name === 'notfound') return null;
  if (PUBLIC.includes(route.name)) {
    return route.name === 'login' && ctx.authenticated ? '/' : null;
  }
  if (!ctx.authenticated) return '/login';
  if (ADMIN_ONLY.includes(route.name) && !ctx.isAdmin) return '/';
  return null;
}

type Listener = (r: Route) => void;
const listeners = new Set<Listener>();
let ctxProvider: () => GuardContext = () => ({
  authenticated: false,
  needsSetup: null,
  isAdmin: false,
});
let current: Route = matchRoute(location.pathname);

export function setGuardContext(fn: () => GuardContext): void {
  ctxProvider = fn;
}

export function currentRoute(): Route {
  return current;
}

export function subscribe(fn: Listener): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

function resolve(path: string): Route {
  let route = matchRoute(path);
  for (let i = 0; i < 5; i++) {
    const to = guard(route, ctxProvider());
    if (to === null || to === route.path) break;
    route = matchRoute(to);
  }
  return route;
}

function apply(route: Route): void {
  current = route;
  for (const l of listeners) l(route);
}

/** Re-evaluates guards for the current URL (call after auth state changes). */
export function revalidate(): void {
  const from = location.pathname + location.search;
  const route = resolve(location.pathname);
  const target = redirectTarget(from, location.pathname + location.search + location.hash, route);
  if (target !== location.pathname + location.search + location.hash) {
    history.replaceState(null, '', target);
  }
  apply(route);
}

/** URL to show for a resolved route; sign-in redirects remember where the user was going. */
function redirectTarget(from: string, full: string, route: Route): string {
  const fromPath = from.split('?')[0] as string;
  if (route.path === fromPath) return full;
  if (route.name === 'login' && matchRoute(fromPath).name !== 'login') {
    return '/login?next=' + encodeURIComponent(full);
  }
  return route.path;
}

export function navigate(to: string, opts: { replace?: boolean } = {}): void {
  const url = new URL(to, location.origin);
  const route = resolve(url.pathname);
  const target = redirectTarget(
    url.pathname + url.search,
    url.pathname + url.search + url.hash,
    route,
  );
  if (opts.replace) history.replaceState(null, '', target);
  else history.pushState(null, '', target);
  apply(route);
  focusMainHeading();
}

/** Moves focus to the page's main heading so screen readers announce the change. */
export function focusMainHeading(): void {
  queueMicrotask(() => {
    const h = document.querySelector<HTMLElement>('main h1');
    if (!h) return;
    // A drawer that already took focus (thread composer) keeps it.
    if (document.activeElement?.closest('[data-region="drawer"]')) return;
    if (!h.hasAttribute('tabindex')) h.setAttribute('tabindex', '-1');
    h.focus();
  });
}

function hasControlChar(s: string): boolean {
  for (const ch of s) {
    const c = ch.charCodeAt(0);
    if (c < 0x20 || c === 0x7f) return true;
  }
  return false;
}

/** Only same-origin absolute paths are accepted as post-login targets. */
export function safeNext(next: string | null): string {
  if (!next || !next.startsWith('/') || next.startsWith('//')) return '/';
  if (next.includes(String.fromCharCode(92)) || hasControlChar(next)) return '/';
  try {
    return new URL(next, location.origin).origin === location.origin ? next : '/';
  } catch {
    return '/';
  }
}

function isClientPath(url: URL): boolean {
  if (url.origin !== location.origin) return false;
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/assets/')) return false;
  const last = url.pathname.split('/').pop() ?? '';
  return !last.includes('.');
}

/** Intercepts plain left-clicks on same-origin client links. Returns an uninstaller. */
export function installRouter(): () => void {
  const onClick = (e: MouseEvent) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) {
      return;
    }
    const a = (e.target as Element | null)?.closest?.('a');
    if (!a || !a.hasAttribute('href') || a.target || a.hasAttribute('download')) return;
    const url = new URL(a.href, location.href);
    if (!isClientPath(url)) return;
    e.preventDefault();
    navigate(url.pathname + url.search + url.hash);
  };
  const onPop = () => {
    revalidate();
    focusMainHeading();
  };
  document.addEventListener('click', onClick);
  window.addEventListener('popstate', onPop);
  revalidate();
  return () => {
    document.removeEventListener('click', onClick);
    window.removeEventListener('popstate', onPop);
  };
}
