// Slash commands the composer can invoke (built-ins and app commands).
import { api, call } from '../api/client';
import type { Schemas } from '../api/client';
import { onLocalReset } from '../reset';

export type Command = Schemas['Command'];

class CommandStore {
  items = $state<Command[]>([]);
  loaded = $state(false);
  #loading: Promise<void> | null = null;

  get names(): string[] {
    return this.items.map((c) => c.name);
  }

  /** Loads the list once; a failure leaves it empty and retries on the next call. */
  load(): Promise<void> {
    this.#loading ??= call(api.GET('/commands'))
      .then((r) => {
        this.items = r.items;
        this.loaded = true;
      })
      .catch(() => {
        this.#loading = null;
      });
    return this.#loading;
  }

  clear(): void {
    this.items = [];
    this.loaded = false;
    this.#loading = null;
  }
}

export const commands = new CommandStore();

onLocalReset(() => commands.clear());
