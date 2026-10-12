<script lang="ts">
  import { onMount } from 'svelte';
  import { summarizeNew } from '../../lib/a11y/announce';
  import type { Message } from '../../lib/api/types';
  import { senderName } from '../../lib/message-actions';
  import { messages } from '../../lib/state/messages.svelte';
  import { session } from '../../lib/state/session.svelte';

  /** Announces new messages of the current channel only, summarised every 3 s. */
  let { channelId }: { channelId: string } = $props();

  const FLUSH_MS = 3000;

  let text = $state('');
  let batch: Message[] = [];
  // eslint-disable-next-line svelte/prefer-svelte-reactivity -- plain bookkeeping, not rendered
  let seen = new Set<string>();
  let timer: ReturnType<typeof setTimeout> | null = null;

  function flush() {
    timer = null;
    text = summarizeNew(batch, (m) => senderName(m).name);
    batch = [];
  }

  let lastChannel = '';
  const FRESH_MS = 60_000;

  // Switching channels forgets everything: only fresh arrivals after that count.
  $effect(() => {
    const id = channelId;
    const list = messages.list(id);
    if (id !== lastChannel) {
      lastChannel = id;
      seen = new Set(list.map((m) => m.id));
      batch = [];
      text = '';
      return;
    }
    for (const m of list) {
      if (seen.has(m.id)) continue;
      seen.add(m.id);
      const fresh = Date.now() - new Date(m.created_at).getTime() < FRESH_MS;
      if (!fresh || m.deleted || m.author.id === session.me?.id || m.kind === 'system') continue;
      batch.push(m);
    }
    if (batch.length > 0 && timer === null) timer = setTimeout(flush, FLUSH_MS);
  });

  onMount(() => () => {
    if (timer) clearTimeout(timer);
  });
</script>

<div class="visually-hidden" aria-live="polite" role="status">{text}</div>
