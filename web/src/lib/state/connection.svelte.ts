import type { ConnState } from '../ws/client';

/** Network and realtime status shown by the ConnectionBanner. */
class ConnectionState {
  /** navigator.onLine */
  online = $state(true);
  /** Server answered 503 resetting. */
  resetting = $state(false);
  ws = $state<ConnState>('idle');
  /** Set by the realtime controller: connect immediately. */
  retry: (() => void) | null = null;

  get reconnecting(): boolean {
    return this.ws === 'backoff' || this.ws === 'connecting';
  }
}

export const connection = new ConnectionState();
