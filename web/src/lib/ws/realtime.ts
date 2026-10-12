// Wires the WebSocket client to state, local reset, router and the page's
// visibility. Started after sign-in or boot, stopped on sign-out.
import { loadBootstrap } from '../bootstrap';
import { resetLocalState } from '../reset';
import { navigate } from '../router';
import { applyEvent, dispatchHooks } from '../state/dispatch';
import { connection } from '../state/connection.svelte';
import { session } from '../state/session.svelte';
import { WsClient } from './client';

let client: WsClient | null = null;
let teardown: (() => void) | null = null;
let focusedChannel: string | null = null;

export function getClient(): WsClient | null {
  return client;
}

/** The channel the user is looking at (null = none); drives focus frames. */
export function setFocusedChannel(channelId: string | null): void {
  focusedChannel = channelId;
  sendFocus();
}

function sendFocus(): void {
  client?.sendFocus(focusedChannel, document.visibilityState === 'visible');
}

async function refetch(): Promise<void> {
  try {
    const b = await loadBootstrap();
    client?.setResumeState({ seq: b.seq_head, instanceId: b.instance_id, epoch: b.epoch });
  } catch {
    // The api hooks surface 401/503; the socket keeps running meanwhile.
  }
}

export function stopRealtime(): void {
  teardown?.();
  teardown = null;
  client?.stop();
  client = null;
  connection.ws = 'idle';
}

/** Loads /bootstrap, then opens the socket with its resume state. */
export async function startRealtime(): Promise<void> {
  stopRealtime();
  const b = await loadBootstrap();
  const c = new WsClient({
    handlers: {
      onEvent: applyEvent,
      onState: (s) => (connection.ws = s),
      onHello: (h) => {
        if (h.resume === 'ok') sendFocus();
      },
      onResync: async () => {
        await resetLocalState('content');
        await refetch();
        sendFocus();
      },
      onFactoryReset: () => void resetLocalState('factory'),
      onContentCleared: () => void resetLocalState('content'),
      onSessionRevoked: () => {
        session.clear();
        stopRealtime();
        navigate('/login', { replace: true });
      },
    },
  });
  c.setResumeState({ seq: b.seq_head, instanceId: b.instance_id, epoch: b.epoch });
  client = c;
  dispatchHooks.onSettingsUpdated = () => void refetch();
  connection.retry = () => c.reconnectNow();

  const onVis = () => sendFocus();
  document.addEventListener('visibilitychange', onVis);
  window.addEventListener('focus', onVis);
  window.addEventListener('blur', onVis);
  teardown = () => {
    document.removeEventListener('visibilitychange', onVis);
    window.removeEventListener('focus', onVis);
    window.removeEventListener('blur', onVis);
    connection.retry = null;
  };
  c.connect();
}
