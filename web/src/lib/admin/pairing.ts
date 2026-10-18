// Choices of the pairing approval form (routes/admin/Pairing.svelte).
import type { Channel } from '../api/types';
import type { Scope } from './scopes';

/** An app that asks for alerts:write needs a channel to post in, so the
 *  approval form starts with "Also create a channel" ticked for it. */
export function createChannelByDefault(requested: readonly Scope[]): boolean {
  return requested.includes('alerts:write');
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
