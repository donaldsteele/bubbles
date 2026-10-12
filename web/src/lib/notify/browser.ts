// Browser (Notification API) notifications, pause handling.
import { settings } from '../state/settings.svelte';

export type BrowserPermission = NotificationPermission | 'unsupported';

export function browserPermission(): BrowserPermission {
  return typeof Notification === 'undefined' ? 'unsupported' : Notification.permission;
}

/** Asks the browser for permission. Call only from a user click. */
export async function requestBrowserPermission(): Promise<BrowserPermission> {
  if (typeof Notification === 'undefined') return 'unsupported';
  try {
    return await Notification.requestPermission();
  } catch {
    return Notification.permission;
  }
}

export type PauseKind = '1h' | 'tomorrow';

/** Unix ms at which a pause of the given kind ends: +1 h, or 08:00 local tomorrow. */
export function pauseUntil(kind: PauseKind, now: number): number {
  if (kind === '1h') return now + 3_600_000;
  const d = new Date(now);
  d.setDate(d.getDate() + 1);
  d.setHours(8, 0, 0, 0);
  return d.getTime();
}

export function isPaused(now: number = Date.now()): boolean {
  return settings.pausedUntil > now;
}

export interface ShownNotification {
  title: string;
  body: string;
  /** Replaces an earlier notification with the same tag. */
  tag: string;
  /** Lets the service worker route a click when it shows the notification. */
  channelId?: string;
  messageId?: string;
  onclick: () => void;
}

/** True when a service worker controls the page and can show notifications. */
function controlled(): boolean {
  return typeof navigator !== 'undefined' && !!navigator.serviceWorker?.controller;
}

/**
 * Shows a notification when the user opted in and the browser allows it. With
 * a controlling service worker the worker shows it (Android Chrome rejects
 * the page constructor); its notificationclick handler then routes the click.
 */
export function showBrowserNotification(n: ShownNotification): void {
  if (!settings.browserNotify || browserPermission() !== 'granted') return;
  if (controlled() && n.channelId) {
    const data = { channel_id: n.channelId, message_id: n.messageId };
    void navigator.serviceWorker
      .getRegistration()
      .then((reg) => {
        if (!reg) throw new Error('no registration');
        return reg.showNotification(n.title, { body: n.body, tag: n.tag, data, icon: ICON });
      })
      .catch(() => showWithConstructor(n));
    return;
  }
  showWithConstructor(n);
}

const ICON = '/icons/icon-192.png';

function showWithConstructor(n: ShownNotification): void {
  try {
    const note = new Notification(n.title, { body: n.body, tag: n.tag });
    note.onclick = () => {
      window.focus();
      n.onclick();
      note.close();
    };
  } catch {
    // some browsers only allow notifications from a service worker
  }
}
