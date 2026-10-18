// Choices of the pairing approval form (routes/admin/Pairing.svelte).
import type { Schemas } from '../api/client';
import type { Channel } from '../api/types';
import type { Scope } from './scopes';

/** The existing app a pairing request would replace. */
export type Replaces = Schemas['PairingReplaces'];

/** The username the server derives from an app name (deriveUsername in
 *  internal/apps/apps.go): lower-case ASCII letters, digits, '.' and '_', with
 *  runs of anything else turned into one hyphen. */
export function appUsername(appName: string): string {
  let out = '';
  let dash = false;
  for (const ch of appName.toLowerCase()) {
    if ((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch === '.' || ch === '_') {
      out += ch;
      dash = false;
    } else {
      if (!dash && out.length > 0) out += '-';
      dash = true;
    }
  }
  const trim = (s: string) => s.replace(/^[-._]+|[-._]+$/g, '');
  let u = trim(out);
  if (u.length > 32) u = trim(u.slice(0, 32));
  if (u.length < 2) u = ('app-' + u).replace(/-+$/, '');
  return u;
}

/** The alerts channel of a bot username (alertsChannelName in
 *  internal/apps/pairing.go). */
export function alertsChannelOf(username: string): string {
  return username.replace(/[._]/g, '-') + '-alerts';
}

/** The name of the channel "Also create a channel" creates for a new app.
 *  When the app's username is taken the server adds a suffix ("-2"), which
 *  this cannot know. */
export function alertsChannelName(appName: string): string {
  return alertsChannelOf(appUsername(appName));
}

/** An app that asks for alerts:write needs a channel to post in, so the
 *  approval form starts with "Also create a channel" ticked for it, unless a
 *  public or private channel (archived ones included) already has the name
 *  the server would give it. When the request reconnects an existing app
 *  (replacing), its username decides the name, and a bot that is already in
 *  a channel needs no new one. */
export function createChannelByDefault(
  requested: readonly Scope[],
  appName: string,
  channels: readonly Channel[],
  replacing: Replaces | null = null,
): boolean {
  if (!requested.includes('alerts:write')) return false;
  if (replacing && replacing.channels.length > 0) return false;
  const name = replacing ? alertsChannelOf(replacing.username) : alertsChannelName(appName);
  return !channels.some((c) => (c.kind === 'public' || c.kind === 'private') && c.name === name);
}

/** The channels the bot can be added to on approval: those that are not
 *  archived, since adding a member to an archived channel fails. */
export function joinableChannels(channels: readonly Channel[]): Channel[] {
  return channels.filter((c) => c.archived_at === null);
}

/** True when an approval would grant alerts:write while leaving the bot in no
 *  channel: no channel to create, no ticked channel that is not archived,
 *  and (when reconnecting an existing app) no channel the bot is already in.
 *  The form then warns the admin; approving is still allowed. */
export function alertsBotWithoutChannel(
  scopes: readonly Scope[],
  picked: readonly string[],
  channels: readonly Channel[],
  createChannel: boolean,
  replacing: Replaces | null = null,
): boolean {
  if (!scopes.includes('alerts:write') || createChannel) return false;
  if (replacing && replacing.channels.length > 0) return false;
  return !channels.some((c) => picked.includes(c.id) && c.archived_at === null);
}
