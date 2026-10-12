// Registers the service worker after the app has booted and wires the update
// prompt and notification-click navigation to it.
import { navigate, safeNext } from '../router';
import { isNavigateMessage } from './sw-core';
import { pwaUpdate } from './update.svelte';

const UPDATE_CHECK_MS = 60 * 60 * 1000;

function watch(reg: ServiceWorkerRegistration): void {
  pwaUpdate.activate = () => reg.waiting?.postMessage({ type: 'SKIP_WAITING' });
  if (reg.waiting && navigator.serviceWorker.controller) pwaUpdate.send('waiting');
  reg.addEventListener('updatefound', () => {
    const next = reg.installing;
    next?.addEventListener('statechange', () => {
      if (next.state === 'installed' && navigator.serviceWorker.controller) {
        pwaUpdate.send('waiting');
      }
    });
  });
  setInterval(() => void reg.update().catch(() => {}), UPDATE_CHECK_MS);
}

export async function registerServiceWorker(): Promise<void> {
  if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return;
  if (import.meta.env.DEV) return;
  const sw = navigator.serviceWorker;
  sw.addEventListener('controllerchange', () => {
    if (pwaUpdate.send('controllerchange') === 'reloading') location.reload();
  });
  sw.addEventListener('message', (e: MessageEvent) => {
    if (isNavigateMessage(e.data)) navigate(safeNext(e.data.path));
  });
  try {
    watch(await sw.register('/sw.js', { scope: '/' }));
  } catch {
    // no worker (insecure context, blocked): the app works without it
  }
}
