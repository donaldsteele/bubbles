import createClient, { type Middleware } from 'openapi-fetch';
import type { components, paths } from './schema';

export type Schemas = components['schemas'];

export type ErrorCode =
  | Schemas['ErrorDetail']['code']
  // Sent with 503 while the server runs a reset.
  | 'resetting'
  | 'network'
  | 'unknown';

/** Typed form of the server error envelope {"error":{code,message,details}}. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: ErrorCode;
  readonly details: Record<string, unknown> | undefined;
  readonly retryAfter: number | undefined;

  constructor(
    status: number,
    code: ErrorCode,
    message: string,
    details?: Record<string, unknown>,
    retryAfter?: number,
  ) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.details = details;
    this.retryAfter = retryAfter;
  }

  /** Per-field validation reasons from details.fields. */
  get fields(): Record<string, string> {
    const f = this.details?.fields;
    if (f && typeof f === 'object') {
      const out: Record<string, string> = {};
      for (const [k, v] of Object.entries(f)) if (typeof v === 'string') out[k] = v;
      return out;
    }
    return {};
  }
}

/** Builds an ApiError from a response status and (possibly absent) JSON body. */
export function toApiError(status: number, body: unknown, response?: Response): ApiError {
  const e = (body as { error?: Partial<Schemas['ErrorDetail']> } | null | undefined)?.error;
  const ra = Number(response?.headers.get('Retry-After'));
  const retryAfter = Number.isFinite(ra) && ra > 0 ? ra : undefined;
  if (e && typeof e.code === 'string' && typeof e.message === 'string') {
    return new ApiError(status, e.code as ErrorCode, e.message, e.details, retryAfter);
  }
  return new ApiError(status, 'unknown', `HTTP ${status}`, undefined, retryAfter);
}

export interface ApiHooks {
  /** true while the server answers 503 resetting; false after a later success. */
  onResetting(active: boolean): void;
  /** A request that needed a session got 401. */
  onUnauthorized(): void;
}

export const apiHooks: ApiHooks = {
  onResetting: () => {},
  onUnauthorized: () => {},
};

// Endpoints where 401 means "wrong credentials", not "session expired".
const PUBLIC_PREFIXES = ['/auth/', '/status', '/invites/', '/register', '/password-resets/'];

export function isResetting(err: ApiError): boolean {
  return err.status === 503 && (err.code === 'resetting' || err.code === 'unavailable');
}

const hooksMiddleware: Middleware = {
  async onResponse({ request, response }) {
    if (response.status === 503) {
      const body: unknown = await response
        .clone()
        .json()
        .catch(() => null);
      if (isResetting(toApiError(503, body))) apiHooks.onResetting(true);
    } else {
      apiHooks.onResetting(false);
      if (response.status === 401) {
        const path = new URL(request.url, location.origin).pathname.replace(/^\/api\/v1/, '');
        if (!PUBLIC_PREFIXES.some((p) => path.startsWith(p))) apiHooks.onUnauthorized();
      }
    }
    return undefined;
  },
};

export function createApi(fetchImpl?: typeof fetch) {
  const client = createClient<paths>({
    baseUrl: `${location.origin}/api/v1`,
    credentials: 'same-origin',
    ...(fetchImpl ? { fetch: fetchImpl } : {}),
  });
  client.use(hooksMiddleware);
  return client;
}

export const api = createApi();

interface Result<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

/** Returns the data of an openapi-fetch result or throws a typed ApiError. */
export function unwrap<T>(res: Result<T>): T {
  if (res.error !== undefined || !res.response.ok) {
    throw toApiError(res.response.status, res.error, res.response);
  }
  return res.data as T;
}

/** Runs a request; transport failures become ApiError('network'). */
export async function call<T>(req: Promise<Result<T>>): Promise<T> {
  let res: Result<T>;
  try {
    res = await req;
  } catch (err) {
    if (err instanceof ApiError) throw err;
    throw new ApiError(0, 'network', 'network error');
  }
  return unwrap(res);
}
