// Choices of the pairing approval form (routes/admin/Pairing.svelte).
import type { Channel } from '../api/types';
import type { Scope } from './scopes';

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

/** The name of the channel "Also create a channel" creates for an app
 *  (alertsChannelName in internal/apps/pairing.go). When the app's username
 *  is taken the server adds a suffix ("-2"), which this cannot know. */
export function alertsChannelName(appName: string): string {
  return appUsername(appName).replace(/[._]/g, '-') + '-alerts';
}

/** An app that asks for alerts:write needs a channel to post in, so the
 *  approval form starts with "Also create a channel" ticked for it, unless a
 *  public or private channel (archived ones included) already has the name
 *  the server would give it: creating it would then fail. */
export function createChannelByDefault(
  requested: readonly Scope[],
  appName: string,
  channels: readonly Channel[],
): boolean {
  if (!requested.includes('alerts:write')) return false;
  const name = alertsChannelName(appName);
  return !channels.some((c) => (c.kind === 'public' || c.kind === 'private') && c.name === name);
}

/** The channels the bot can be added to on approval: those that are not
 *  archived, since adding a member to an archived channel fails. */
export function joinableChannels(channels: readonly Channel[]): Channel[] {
  return channels.filter((c) => c.archived_at === null);
}

/** True when an approval would grant alerts:write while leaving the bot in no
 *  channel: no channel to create and no ticked channel that is not archived.
 *  The form then warns the admin; approving is still allowed. */
export function alertsBotWithoutChannel(
  scopes: readonly Scope[],
  picked: readonly string[],
  channels: readonly Channel[],
  createChannel: boolean,
): boolean {
  if (!scopes.includes('alerts:write') || createChannel) return false;
  return !channels.some((c) => picked.includes(c.id) && c.archived_at === null);
}
