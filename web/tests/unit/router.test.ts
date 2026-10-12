import { beforeEach, describe, expect, it } from 'vitest';
import {
  guard,
  matchRoute,
  navigate,
  safeNext,
  setGuardContext,
  currentRoute,
  type GuardContext,
} from '../../src/lib/router';

const anon: GuardContext = { authenticated: false, needsSetup: false, isAdmin: false };
const member: GuardContext = { authenticated: true, needsSetup: false, isAdmin: false };
const admin: GuardContext = { authenticated: true, needsSetup: false, isAdmin: true };

describe('matchRoute', () => {
  it.each([
    ['/', 'home'],
    ['/setup', 'setup'],
    ['/login', 'login'],
    ['/invite/abc', 'invite'],
    ['/reset/xyz', 'reset'],
    ['/c/01A', 'channel'],
    ['/c/01A/t/01B', 'thread'],
    ['/admin', 'admin'],
    ['/admin/users', 'admin'],
    ['/settings/profile', 'settings'],
    ['/nope', 'notfound'],
    ['/c/01A/extra', 'notfound'],
  ])('%s -> %s', (path, name) => {
    expect(matchRoute(path).name).toBe(name);
  });

  it('extracts params and the rest path', () => {
    expect(matchRoute('/c/01A/t/01B').params).toEqual({ channelId: '01A', messageId: '01B' });
    expect(matchRoute('/invite/a%20b').params.code).toBe('a b');
    expect(matchRoute('/admin/users/1').rest).toBe('users/1');
  });
});

describe('guard', () => {
  it('sends unauthenticated users to /login', () => {
    expect(guard(matchRoute('/c/01A'), anon)).toBe('/login');
    expect(guard(matchRoute('/settings'), anon)).toBe('/login');
    expect(guard(matchRoute('/'), anon)).toBe('/login');
  });
  it('lets public pages through', () => {
    for (const p of ['/login', '/invite/x', '/reset/x']) {
      expect(guard(matchRoute(p), anon)).toBeNull();
    }
  });
  it('sends everything to /setup while setup is needed', () => {
    const ctx = { ...anon, needsSetup: true };
    expect(guard(matchRoute('/login'), ctx)).toBe('/setup');
    expect(guard(matchRoute('/c/1'), ctx)).toBe('/setup');
    expect(guard(matchRoute('/setup'), ctx)).toBeNull();
  });
  it('keeps /setup unreachable once set up', () => {
    expect(guard(matchRoute('/setup'), anon)).toBe('/login');
    expect(guard(matchRoute('/setup'), member)).toBe('/');
  });
  it('redirects signed-in users away from /login', () => {
    expect(guard(matchRoute('/login'), member)).toBe('/');
  });
  it('limits /admin to admins', () => {
    expect(guard(matchRoute('/admin/users'), member)).toBe('/');
    expect(guard(matchRoute('/admin/users'), admin)).toBeNull();
  });
  it('shows NotFound without a redirect', () => {
    expect(guard(matchRoute('/zzz'), anon)).toBeNull();
  });
});

describe('navigate', () => {
  beforeEach(() => setGuardContext(() => anon));

  it('applies guards and remembers the intended target', () => {
    navigate('/c/01A?x=1');
    expect(location.pathname).toBe('/login');
    expect(new URLSearchParams(location.search).get('next')).toBe('/c/01A?x=1');
    expect(currentRoute().name).toBe('login');
  });

  it('navigates when allowed and moves focus to the main heading', async () => {
    setGuardContext(() => member);
    document.body.innerHTML = '<main><h1>Title</h1></main>';
    navigate('/settings');
    expect(location.pathname).toBe('/settings');
    await Promise.resolve();
    expect(document.activeElement?.tagName).toBe('H1');
  });
});

describe('safeNext', () => {
  it('accepts only same-origin paths', () => {
    expect(safeNext('/c/1')).toBe('/c/1');
    expect(safeNext('//evil.test')).toBe('/');
    expect(safeNext('https://evil.test')).toBe('/');
    expect(safeNext(null)).toBe('/');
    expect(safeNext(String.raw`/\evil.com`)).toBe('/');
    expect(safeNext('/a\nb')).toBe('/');
    expect(safeNext('/c/1?x=1#h')).toBe('/c/1?x=1#h');
  });
});
