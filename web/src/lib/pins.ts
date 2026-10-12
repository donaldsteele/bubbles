import { ApiError, api, call } from './api/client';
import type { Message } from './api/types';
import { messages } from './state/messages.svelte';

export interface PinEntry {
  message: Message;
  pinned_at: string;
  pinned_by: string | null;
}

export async function listPins(channelId: string): Promise<PinEntry[]> {
  const r = await call(api.GET('/channels/{id}/pins', { params: { path: { id: channelId } } }));
  messages.pins[channelId] = r.items.map((p) => p.message.id);
  return r.items;
}

export async function pinMessage(channelId: string, messageId: string): Promise<void> {
  try {
    await call(
      api.PUT('/channels/{id}/pins/{message_id}', {
        params: { path: { id: channelId, message_id: messageId } },
      }),
    );
  } catch (err) {
    // 409: already pinned
    if (!(err instanceof ApiError && err.status === 409)) throw err;
  }
  messages.applyPin('pin.added', { channel_id: channelId, message_id: messageId });
}

export async function unpinMessage(channelId: string, messageId: string): Promise<void> {
  try {
    await call(
      api.DELETE('/channels/{id}/pins/{message_id}', {
        params: { path: { id: channelId, message_id: messageId } },
      }),
    );
  } catch (err) {
    if (!(err instanceof ApiError && err.status === 409)) throw err;
  }
  messages.applyPin('pin.removed', { channel_id: channelId, message_id: messageId });
}
