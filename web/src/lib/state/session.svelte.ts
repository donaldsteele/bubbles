import type { Me } from '../api/types';
import { onLocalReset } from '../reset';

/** Who is signed in and which server instance/epoch this tab has seen. */
class SessionState {
  me = $state<Me | null>(null);
  instanceId = $state('');
  epoch = $state(0);
  seqHead = $state(0);
  /** The instance name from GET /status; empty until it answered. */
  instanceName = $state('');
  /** Whether self-sign-up is open right now (GET /status). */
  registrationOpen = $state(false);
  registrationClosesAt = $state<string | null>(null);
  /** true once the first boot attempt (status + /me) finished. */
  ready = $state(false);

  get authenticated(): boolean {
    return this.me !== null;
  }

  get isAdmin(): boolean {
    return this.me?.role === 'admin';
  }

  setMe(me: Me): void {
    this.me = me;
  }

  setInstance(instanceId: string, epoch: number, seqHead: number): void {
    this.instanceId = instanceId;
    this.epoch = epoch;
    this.seqHead = seqHead;
  }

  /** Signed out or session expired; instance info is kept. */
  clear(): void {
    this.me = null;
  }
}

export const session = new SessionState();

onLocalReset((level) => {
  if (level === 'factory') {
    session.clear();
    session.setInstance('', 0, 0);
  }
});
