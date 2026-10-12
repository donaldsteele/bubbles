import type { MarkdownEnv } from './plugins';
import { channels } from '../state/channels.svelte';
import { users } from '../state/users.svelte';

/** Reactive name lookups for the Markdown renderer. */
class LiveEnv implements MarkdownEnv {
  #byName = $derived.by(() => {
    // eslint-disable-next-line svelte/prefer-svelte-reactivity -- rebuilt inside $derived
    const m = new Map<string, { id: string }>();
    for (const u of Object.values(users.byId)) {
      if (!u.deactivated) m.set(u.username.toLowerCase(), { id: u.id });
    }
    return m;
  });
  #channels = $derived.by(() => {
    // eslint-disable-next-line svelte/prefer-svelte-reactivity -- rebuilt inside $derived
    const m = new Map<string, { id: string }>();
    for (const c of channels.joined) {
      if ((c.kind === 'public' || c.kind === 'private') && c.name) {
        m.set(c.name.toLowerCase(), { id: c.id });
      }
    }
    return m;
  });

  user = (name: string) => this.#byName.get(name);
  channel = (name: string) => this.#channels.get(name);
}

export const markdownEnv = new LiveEnv();
