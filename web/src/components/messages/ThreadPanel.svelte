<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { X } from '@lucide/svelte';
  import { api, call } from '../../lib/api/client';
  import type { Message } from '../../lib/api/types';
  import { pendingMessage } from '../../lib/message-actions';
  import { outbox } from '../../lib/composer/outbox.svelte';
  import { t } from '../../lib/i18n/en';
  import { focusRegion } from '../../lib/keyboard';
  import { buildRows } from '../../lib/messages/layout';
  import { navigate } from '../../lib/router';
  import { channels } from '../../lib/state/channels.svelte';
  import { messages } from '../../lib/state/messages.svelte';
  import { session } from '../../lib/state/session.svelte';
  import { ui } from '../../lib/state/ui.svelte';
  import Composer from './Composer.svelte';
  import EphemeralNotice from './EphemeralNotice.svelte';
  import MessageItem from './MessageItem.svelte';

  let { channelId, rootId }: { channelId: string; rootId: string } = $props();

  let fetchedRoot = $state<Message | null>(null);
  let failed = $state(false);
  let list: HTMLDivElement;

  const channelName = $derived(channels.byId[channelId]?.name ?? '');
  const root = $derived(messages.find(channelId, rootId) ?? fetchedRoot);
  const replies = $derived(messages.thread(rootId));
  const rows = $derived(buildRows(replies));
  const queued = $derived(outbox.forScope(channelId, rootId));

  $effect(() => {
    const id = rootId;
    fetchedRoot = null;
    failed = false;
    call(api.GET('/messages/{id}/thread', { params: { path: { id } } }))
      .then((th) => {
        if (id !== rootId) return;
        fetchedRoot = th.root;
        for (const r of th.replies) messages.upsert(r);
      })
      .catch(() => {
        if (id === rootId) failed = true;
      });
  });

  // Keep the newest reply in view.
  $effect(() => {
    void replies.length;
    void queued.length;
    void tick().then(() => list && (list.scrollTop = list.scrollHeight));
  });

  function close() {
    const trigger = ui.drawerTrigger;
    ui.drawerTrigger = null;
    navigate(`/c/${channelId}`);
    void tick().then(() => {
      if (trigger?.isConnected) {
        // The row's actions are hidden until focus is inside the row.
        trigger.closest<HTMLElement>('article')?.focus();
        trigger.focus();
      } else focusRegion('messages');
    });
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && !e.defaultPrevented) {
      e.preventDefault();
      close();
    }
  }

  onMount(() => {
    const box = document.querySelector<HTMLElement>('[data-region="drawer-composer"]');
    box?.focus();
  });
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<section class="panel" aria-labelledby="thread-title" {onkeydown}>
  <header>
    <h2 id="thread-title">{t.chat.threadTitle}</h2>
    <button
      class="icon"
      type="button"
      aria-label={t.chat.close}
      title={t.chat.close}
      onclick={close}
    >
      <X aria-hidden="true" size={18} />
    </button>
  </header>
  <div class="list" bind:this={list}>
    {#if failed}
      <p class="status" role="alert">{t.chat.loadFailed}</p>
    {/if}
    {#if root}
      <MessageItem message={root} inThread />
    {/if}
    <h3 id="thread-replies-title" class="sub">{t.chat.threadList}</h3>
    {#if replies.length === 0 && queued.length === 0}
      <p class="status">{t.chat.noReplies}</p>
    {/if}
    <ul role="list" aria-labelledby="thread-replies-title">
      {#each rows as row (row.key)}
        {#if row.type === 'msg'}
          <li><MessageItem message={row.message} grouped={row.grouped} inThread /></li>
        {/if}
      {/each}
      {#each queued as item (item.nonce)}
        <li><MessageItem message={pendingMessage(item, session.me)} pending={item} inThread /></li>
      {/each}
    </ul>
    <EphemeralNotice {channelId} threadRootId={rootId} />
  </div>
  <Composer {channelId} {channelName} threadRootId={rootId} region="drawer-composer" />
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
  .sub {
    margin: var(--s3) var(--s4) var(--s1);
    font-size: 12px;
    text-transform: uppercase;
    color: var(--fg-muted);
  }
  .list {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .status {
    margin: var(--s2) var(--s4);
    color: var(--fg-muted);
  }
  .icon {
    min-width: 44px;
    min-height: 44px;
    display: grid;
    place-items: center;
    background: none;
    border: 0;
    cursor: pointer;
  }
</style>
