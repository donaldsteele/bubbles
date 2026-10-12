<script lang="ts">
  import { PinOff, X } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import { t } from '../../lib/i18n/en';
  import { clockTime } from '../../lib/messages/time';
  import { listPins, unpinMessage, type PinEntry } from '../../lib/pins';
  import { navigate } from '../../lib/router';
  import { messages } from '../../lib/state/messages.svelte';
  import { ui } from '../../lib/state/ui.svelte';
  import Markdown from '../messages/Markdown.svelte';

  let { channelId, onclose }: { channelId: string; onclose: () => void } = $props();

  let entries = $state<PinEntry[]>([]);
  let loading = $state(true);
  let failed = $state(false);

  // Live pin changes drop entries immediately; new pins trigger a reload.
  const ids = $derived(messages.pins[channelId] ?? []);
  const shown = $derived(entries.filter((e) => ids.includes(e.message.id)));

  async function load() {
    loading = true;
    failed = false;
    try {
      entries = await listPins(channelId);
    } catch {
      failed = true;
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    void load();
  });

  $effect(() => {
    if (!loading && ids.some((id) => !entries.some((e) => e.message.id === id))) void load();
  });

  function show(e: PinEntry) {
    ui.jump = { channelId, messageId: e.message.id };
    navigate(`/c/${channelId}`);
  }

  async function unpin(e: PinEntry) {
    try {
      await unpinMessage(channelId, e.message.id);
    } catch {
      failed = true;
    }
  }

  function onkeydown(ev: KeyboardEvent) {
    if (ev.key === 'Escape') {
      ev.preventDefault();
      onclose();
    }
  }
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<section class="panel" aria-labelledby="pins-title" {onkeydown}>
  <header>
    <h2 id="pins-title">{t.rich.pinsTitle}</h2>
    <button
      class="icon"
      type="button"
      data-autofocus
      aria-label={t.rich.close}
      title={t.rich.close}
      onclick={onclose}
    >
      <X aria-hidden="true" size={18} />
    </button>
  </header>
  <div class="list">
    {#if loading}
      <p class="status" role="status">{t.app.loading}</p>
    {:else if failed}
      <p class="status" role="alert">{t.chat.loadFailed}</p>
    {:else if shown.length === 0}
      <p class="status">{t.rich.noPins}</p>
    {/if}
    <ul role="list">
      {#each shown as e (e.message.id)}
        <li>
          <div class="meta">
            <strong>{e.message.author.display_name || e.message.author.username}</strong>
            <time datetime={e.message.created_at}>{clockTime(e.message.created_at)}</time>
          </div>
          <Markdown source={e.message.body} />
          <div class="row">
            <button class="btn" type="button" onclick={() => show(e)}>{t.chat.open}</button>
            <button class="btn" type="button" onclick={() => unpin(e)}>
              <PinOff aria-hidden="true" size={14} />
              {t.rich.unpin}
            </button>
          </div>
        </li>
      {/each}
    </ul>
  </div>
</section>

<style>
  .panel {
    display: flex;
    flex-direction: column;
    height: 100%;
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: var(--s2) var(--s3);
    border-bottom: 1px solid var(--line);
  }
  h2 {
    margin: 0;
    font-size: 16px;
  }
  .list {
    flex: 1;
    overflow-y: auto;
    padding: var(--s2) var(--s3);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    padding: var(--s2) 0;
    border-bottom: 1px solid var(--line);
  }
  .meta {
    display: flex;
    gap: var(--s2);
    align-items: baseline;
  }
  time,
  .status {
    color: var(--fg-muted);
    font-size: 12px;
  }
  .row {
    display: flex;
    gap: var(--s2);
    margin-top: var(--s1);
  }
  .row .btn {
    display: inline-flex;
    align-items: center;
    gap: var(--s1);
    min-height: 28px;
  }
  .icon {
    min-width: 32px;
    min-height: 32px;
    display: grid;
    place-items: center;
    background: none;
    border: 0;
    cursor: pointer;
  }
</style>
