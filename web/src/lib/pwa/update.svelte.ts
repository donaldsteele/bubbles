import { nextUpdateState, type UpdateEvent, type UpdateState } from './update';

class UpdateStore {
  state = $state<UpdateState>('idle');
  /** Asks the waiting worker to activate; set by the registration code. */
  activate: (() => void) | null = null;

  send(event: UpdateEvent): UpdateState {
    this.state = nextUpdateState(this.state, event);
    return this.state;
  }

  apply(): void {
    if (this.send('apply') === 'applying') this.activate?.();
  }
}

export const pwaUpdate = new UpdateStore();
