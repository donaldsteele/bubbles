// Decides, with the shared rules, whether an incoming message or alert update
// notifies this user, and raises the in-app / browser / sound signals.
import type { Message } from '../api/types';
import { navigate } from '../router';
import { channels } from '../state/channels.svelte';
import { session } from '../state/session.svelte';
import { settings } from '../state/settings.svelte';
import { users } from '../state/users.svelte';
import { isPaused, showBrowserNotification } from './browser';
import { decide, type NotifyInput } from './rules';
import { playChime } from './sound';

export interface NotifyContext {
  now: number;
  /** Channel id on screen, or undefined. */
  visibleChannel: string | undefined;
  tabHidden: boolean;
}

/** Builds the rule input for one message from the signed-in user's state. */
export function inputFor(m: Message, transition: string, now: number): NotifyInput | null {
  const meId = session.me?.id;
  const channel = channels.byId[m.channel_id];
  const mem = channels.memberships[m.channel_id];
  if (!meId || !channel || !mem) return null;
  const muted = mem.muted_until ? Date.parse(mem.muted_until) : NaN;
  return {
    recipient_id: meId,
    author_id: m.author.id,
    channel_kind: channel.kind,
    notify_level: mem.notify_level,
    alert_min_severity: mem.alert_min_severity,
    muted_until: Number.isNaN(muted) ? null : muted,
    now,
    mentions: m.mentions ?? { user_ids: [], channel: false, here: false },
    recipient_online: true,
    message_kind: m.kind,
    alert_transition: m.kind === 'alert' ? transition : '',
    alert_severity: m.alert?.severity ?? '',
  };
}

/** True when a notification should be raised for this event. */
export function shouldNotify(m: Message, transition: string, ctx: NotifyContext): boolean {
  if (m.deleted || isPausedAt(ctx.now)) return false;
  const input = inputFor(m, transition, ctx.now);
  if (!input || !decide(input).notify) return false;
  // The conversation on screen in a visible tab needs no alert.
  return ctx.tabHidden || ctx.visibleChannel !== m.channel_id;
}

function isPausedAt(now: number): boolean {
  return isPaused(now);
}

export function handleIncoming(m: Message, transition: string, visibleChannel?: string): void {
  const ctx: NotifyContext = {
    now: Date.now(),
    visibleChannel,
    tabHidden: typeof document !== 'undefined' && document.visibilityState === 'hidden',
  };
  if (!shouldNotify(m, transition, ctx)) return;
  if (settings.sound) playChime();
  const ch = channels.byId[m.channel_id];
  const sender = m.override_name ?? users.get(m.author.id)?.display_name ?? m.author.display_name;
  const where = ch?.name ? `#${ch.name}` : '';
  const text = m.kind === 'alert' && m.alert ? m.alert.title : m.body;
  showBrowserNotification({
    title: where ? `${sender} in ${where}` : sender,
    body: text.length > 120 ? `${text.slice(0, 117)}...` : text,
    tag: m.channel_id,
    channelId: m.channel_id,
    messageId: m.id,
    onclick: () => navigate(`/c/${m.channel_id}`),
  });
}
