// Application lifecycle: first load, sign-in, sign-out and server-reset retry.
import { api, apiHooks, call } from './api/client';
import type { Me } from './api/types';
import { navigate, revalidate, safeNext, setGuardContext } from './router';
import { connection } from './state/connection.svelte';
import { session } from './state/session.svelte';
import { settings } from './state/settings.svelte';
import { installPushResetHook } from './push/subscribe';
import { startRealtime, stopRealtime } from './ws/realtime';

export function wireApp(): void {
  installPushResetHook();
  apiHooks.onResetting = (active) => {
    connection.resetting = active;
  };
  apiHooks.onUnauthorized = () => {
    if (!session.authenticated) return;
    stopRealtime();
    session.clear();
    settings.setViewer('anon');
    revalidate();
  };
  setGuardContext(() => ({
    authenticated: session.authenticated,
    needsSetup: session.needsSetup,
    isAdmin: session.isAdmin,
  }));
}

/** Checks setup status and an existing session, then marks the app ready. */
export async function boot(): Promise<void> {
  settings.setViewer('anon');
  try {
    const st = await call(api.GET('/setup/status'));
    session.needsSetup = st.needs_setup;
    if (!st.needs_setup) await startRealtime();
  } catch {
    // Not signed in, or the server is unreachable: the guards show /login
    // and the banner shows the connection state.
  } finally {
    session.ready = true;
  }
}

/** Called after setup, login, invite accept or password reset sign-in. */
export async function completeSignIn(me: Me, next: string | null = null): Promise<void> {
  session.setMe(me);
  session.needsSetup = false;
  settings.setViewer(me.id);
  try {
    await startRealtime();
  } catch {
    // The shell still loads; the banner reports the connection problem.
  }
  navigate(safeNext(next), { replace: true });
}

export async function signOut(): Promise<void> {
  try {
    await call(api.POST('/auth/logout'));
  } catch {
    // Logout is idempotent server-side; clear locally regardless.
  }
  stopRealtime();
  session.clear();
  settings.setViewer('anon');
  navigate('/login', { replace: true });
}

/** "Retry now" on the connection banner. */
export async function retryServer(): Promise<void> {
  connection.retry?.();
  try {
    await call(api.GET('/setup/status'));
  } catch {
    // the 503 hook keeps the banner up
  }
}
