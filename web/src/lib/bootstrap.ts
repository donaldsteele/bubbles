import { api, call } from './api/client';
import type { Bootstrap } from './api/types';
import { resetLocalState } from './reset';
import { channels } from './state/channels.svelte';
import { session } from './state/session.svelte';
import { settings } from './state/settings.svelte';
import { unread } from './state/unread.svelte';
import { users } from './state/users.svelte';
import { readInstance, writeInstance } from './storage';

/**
 * Applies a /bootstrap payload to the state modules. When the stored
 * instance/epoch differs from the server's, local state is reset first
 * (factory for a different instance, content for a new epoch).
 */
export async function applyBootstrap(b: Bootstrap): Promise<void> {
  const stored = readInstance();
  if (stored && stored.instance_id !== b.instance_id) {
    await resetLocalState('factory');
    return;
  }
  if (stored && stored.epoch !== b.epoch) {
    await resetLocalState('content');
  }
  writeInstance({ instance_id: b.instance_id, epoch: b.epoch });
  session.setMe(b.me);
  session.setInstance(b.instance_id, b.epoch, b.seq_head);
  users.load(b.users);
  channels.load(b.channels, b.memberships);
  unread.load(b.unread);
  settings.public = b.settings_public;
  settings.setViewer(b.me.id);
}

/** GET /bootstrap and apply it. */
export async function loadBootstrap(): Promise<Bootstrap> {
  const b = await call(api.GET('/bootstrap'));
  await applyBootstrap(b);
  return b;
}
