import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError, apiHooks, call, createApi, toApiError } from '../../src/lib/api/client';

function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

afterEach(() => {
  apiHooks.onResetting = () => {};
  apiHooks.onUnauthorized = () => {};
});

describe('toApiError', () => {
  it('maps the error envelope', () => {
    const e = toApiError(400, {
      error: {
        code: 'bad_request',
        message: 'nope',
        details: { fields: { password: 'too short' } },
      },
    });
    expect(e).toBeInstanceOf(ApiError);
    expect(e.status).toBe(400);
    expect(e.code).toBe('bad_request');
    expect(e.message).toBe('nope');
    expect(e.fields).toEqual({ password: 'too short' });
  });

  it('falls back for a non-envelope body and reads Retry-After', () => {
    const e = toApiError(502, '<html>', new Response(null, { headers: { 'Retry-After': '7' } }));
    expect(e.code).toBe('unknown');
    expect(e.retryAfter).toBe(7);
    expect(e.fields).toEqual({});
  });
});

describe('api client', () => {
  it('sends same-origin credentials and throws a typed ApiError', async () => {
    const f = vi.fn((req: Request) => {
      void req;
      return Promise.resolve(json(409, { error: { code: 'conflict', message: 'taken' } }));
    });
    const api = createApi(f as unknown as typeof fetch);
    await expect(call(api.GET('/me'))).rejects.toMatchObject({ status: 409, code: 'conflict' });
    expect((f.mock.calls[0]?.[0] as Request).credentials).toBe('same-origin');
  });

  it('503 resetting raises the global resetting state, and a later success clears it', async () => {
    const states: boolean[] = [];
    apiHooks.onResetting = (a) => states.push(a);
    const responses = [
      json(503, { error: { code: 'unavailable', message: 'resetting' } }),
      json(200, { needs_setup: false }),
    ];
    const api = createApi((async () => responses.shift()) as unknown as typeof fetch);
    await expect(call(api.GET('/setup/status'))).rejects.toMatchObject({ status: 503 });
    await call(api.GET('/setup/status'));
    expect(states).toEqual([true, false]);
  });

  it('401 on a session endpoint signals expiry; 401 from login does not', async () => {
    const onUnauthorized = vi.fn();
    apiHooks.onUnauthorized = onUnauthorized;
    const unauth = () => json(401, { error: { code: 'unauthorized', message: 'no' } });
    const api = createApi((async () => unauth()) as unknown as typeof fetch);
    await expect(call(api.GET('/me'))).rejects.toMatchObject({ status: 401 });
    expect(onUnauthorized).toHaveBeenCalledOnce();
    await expect(
      call(api.POST('/auth/login', { body: { username: 'a', password: 'b' } })),
    ).rejects.toMatchObject({ status: 401 });
    expect(onUnauthorized).toHaveBeenCalledOnce();
  });

  it('transport failures become ApiError(network)', async () => {
    const api = createApi((async () => {
      throw new TypeError('fail');
    }) as unknown as typeof fetch);
    await expect(call(api.GET('/me'))).rejects.toMatchObject({ status: 0, code: 'network' });
  });
});
