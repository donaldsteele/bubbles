<script lang="ts">
  import { t } from '../../lib/i18n/en';
  import { presence } from '../../lib/state/presence.svelte';

  let { userId }: { userId: string } = $props();
  const state = $derived(presence.stateOf(userId));
  const label = $derived(
    state === 'online' ? t.chat.online : state === 'idle' ? t.chat.idle : t.chat.offline,
  );
</script>

<!-- Shape carries the state: filled circle online, half circle idle, ring offline. -->
<span class="dot" class:on={state === 'online'} title={label}>
  <svg width="12" height="12" viewBox="0 0 12 12" aria-hidden="true" focusable="false">
    {#if state === 'online'}
      <circle cx="6" cy="6" r="5" fill="currentColor" />
    {:else if state === 'idle'}
      <circle cx="6" cy="6" r="4.5" fill="none" stroke="currentColor" stroke-width="1.5" />
      <path d="M6 1.5a4.5 4.5 0 0 0 0 9z" fill="currentColor" />
    {:else}
      <circle cx="6" cy="6" r="4.5" fill="none" stroke="currentColor" stroke-width="1.5" />
    {/if}
  </svg>
  <span class="visually-hidden">{label}</span>
</span>

<style>
  .dot {
    display: inline-flex;
    flex: none;
    color: var(--fg-muted);
  }
  .dot.on {
    color: var(--ok);
  }
</style>
