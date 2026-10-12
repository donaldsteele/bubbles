// Web Push subscription management. The toggle is offered only when the server
// publishes a VAPID key and a service worker registration exists.
import { ApiError, api, call } from '../api/client';
import { setPushUnsubscribe } from '../reset';

export type PushAvailability = 'available' | 'unavailable';

function urlBase64ToBytes(v: string): Uint8Array<ArrayBuffer> {
  const pad = '='.repeat((4 - (v.length % 4)) % 4);
  const raw = atob((v + pad).replace(/-/g, '+').replace(/_/g, '/'));
  const out = new Uint8Array(new ArrayBuffer(raw.length));
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

async function registration(): Promise<ServiceWorkerRegistration | null> {
  if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return null;
  try {
    return (await navigator.serviceWorker.getRegistration()) ?? null;
  } catch {
    return null;
  }
}

/** The server's VAPID public key, or null when push is not enabled. */
export async function vapidKey(): Promise<string | null> {
  try {
    const r = await call(api.GET('/push/vapid-public-key'));
    return r.key || null;
  } catch {
    return null;
  }
}

export async function pushAvailability(): Promise<PushAvailability> {
  if (typeof PushManager === 'undefined') return 'unavailable';
  const [key, reg] = await Promise.all([vapidKey(), registration()]);
  return key && reg ? 'available' : 'unavailable';
}

export async function currentSubscription(): Promise<PushSubscription | null> {
  const reg = await registration();
  return reg ? reg.pushManager.getSubscription() : null;
}

export async function subscribePush(): Promise<boolean> {
  const [key, reg] = await Promise.all([vapidKey(), registration()]);
  if (!key || !reg) return false;
  const sub =
    (await reg.pushManager.getSubscription()) ??
    (await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToBytes(key),
    }));
  const json = sub.toJSON();
  const keys = json.keys;
  if (!json.endpoint || !keys?.['p256dh'] || !keys['auth']) return false;
  try {
    await call(
      api.POST('/push/subscriptions', {
        body: { endpoint: json.endpoint, keys: { p256dh: keys['p256dh'], auth: keys['auth'] } },
      }),
    );
  } catch (err) {
    // 409: the server already has this subscription.
    if (!(err instanceof ApiError && err.status === 409)) throw err;
  }
  return true;
}

export async function unsubscribePush(): Promise<void> {
  const sub = await currentSubscription();
  if (!sub) return;
  try {
    await call(api.DELETE('/push/subscriptions', { body: { endpoint: sub.endpoint } }));
  } catch {
    // the server may already have dropped it
  }
  await sub.unsubscribe();
}

/** Lets local resets unsubscribe this browser before the server forgets the user. */
export function installPushResetHook(): void {
  setPushUnsubscribe(async () => {
    try {
      await unsubscribePush();
    } catch {
      // best effort
    }
  });
}
