// Update-prompt state machine. A new worker that finished installing while an
// older one controls the page puts the UI in 'available'; the user's "Reload"
// asks it to take over, and only a controller change that follows reloads.
export type UpdateState = 'idle' | 'available' | 'applying' | 'reloading';
export type UpdateEvent = 'waiting' | 'apply' | 'controllerchange';

export function nextUpdateState(state: UpdateState, event: UpdateEvent): UpdateState {
  switch (event) {
    case 'waiting':
      return state === 'idle' ? 'available' : state;
    case 'apply':
      return state === 'available' ? 'applying' : state;
    case 'controllerchange':
      // First install claims the page without any prompt: never reload then.
      return state === 'applying' ? 'reloading' : state;
  }
}
