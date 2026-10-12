<script lang="ts">
  import { onMount } from 'svelte';
  import { RateGate, TYPING_ANNOUNCE_GAP_MS, typingPhrase } from '../../lib/a11y/announce';
  import { presence } from '../../lib/state/presence.svelte';
  import { session } from '../../lib/state/session.svelte';
  import { users } from '../../lib/state/users.svelte';

  let { channelId }: { channelId: string } = $props();

  let now = $state(Date.now());
  let announced = $state('');
  const gate = new RateGate(TYPING_ANNOUNCE_GAP_MS);

  onMount(() => {
    const id = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(id);
  });

  const names = $derived(
    presence
      .typingIn(channelId, now)
      .filter((id) => id !== session.me?.id)
      .map((id) => users.get(id)?.display_name || users.get(id)?.username || '')
      .filter((n) => n !== ''),
  );
  const phrase = $derived(typingPhrase(names));

  // The live region changes at most once per 10 seconds.
  $effect(() => {
    if (phrase === '') {
      announced = '';
    } else if (phrase !== announced && gate.tryPass(now)) {
      announced = phrase;
    }
  });
</script>

<div class="typing" aria-hidden="true">{phrase}</div>
<div class="visually-hidden" aria-live="polite" role="status">{announced}</div>

<style>
  .typing {
    min-height: 20px;
    padding: 0 var(--s4);
    font-size: 13px;
    color: var(--fg-muted);
  }
</style>
