<script lang="ts">
  import { onMount, tick, untrack } from 'svelte';
  import { ArrowDown } from '@lucide/svelte';
  import { markRead } from '../../lib/channel-actions';
  import { pendingMessage } from '../../lib/message-actions';
  import { outbox } from '../../lib/composer/outbox.svelte';
  import { fmt, t } from '../../lib/i18n/en';
  import { buildRows } from '../../lib/messages/layout';
  import { MessageWindow } from '../../lib/messages/view.svelte';
  import { channels } from '../../lib/state/channels.svelte';
  import { session } from '../../lib/state/session.svelte';
  import { ui } from '../../lib/state/ui.svelte';
  import { unread } from '../../lib/state/unread.svelte';
  import EphemeralNotice from './EphemeralNotice.svelte';
  import DayDivider from './DayDivider.svelte';
  import MessageItem from './MessageItem.svelte';
  import NewDivider from './NewDivider.svelte';

  let { channelId }: { channelId: string } = $props();

  const win = new MessageWindow();
  const NEAR_PX = 150;

  let scroller: HTMLDivElement;
  let atBottom = $state(true);
  let windowFocused = $state(true);
  let lastReadSnapshot = $state<string | null>(null);
  let lastMarked = '';

  const meId = $derived(session.me?.id ?? '');
  const rows = $derived(buildRows(win.items, { lastReadId: lastReadSnapshot, meId }));
  const queued = $derived(win.hasAfter ? [] : outbox.forScope(channelId, null));
  const lastId = $derived(win.items[win.items.length - 1]?.id ?? '');
  const unreadCount = $derived(unread.get(channelId).unread);
  const showJump = $derived(!atBottom || win.hasAfter);

  function scrollToBottom() {
    if (!scroller) return;
    scroller.scrollTop = scroller.scrollHeight;
    atBottom = true;
  }

  function measure() {
    atBottom = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 40;
  }

  async function start(id: string) {
    win.reset();
    lastMarked = '';
    lastReadSnapshot = channels.memberships[id]?.last_read_id ?? null;
    const jump = ui.jump && ui.jump.channelId === id ? ui.jump.messageId : null;
    const around = jump ?? (unread.get(id).unread > 0 ? lastReadSnapshot : null);
    await win.open(id, around);
    if (win.failed && around) await win.open(id, null);
    await tick();
    if (jump) {
      ui.jump = null;
      const target = scroller?.querySelector<HTMLElement>(`[data-id="${CSS.escape(jump)}"]`);
      if (target && !win.failed) {
        target.tabIndex = -1;
        target.scrollIntoView({ block: 'center' });
        target.focus({ preventScroll: true });
        measure();
        return;
      }
    }
    const divider = scroller?.querySelector('#new-divider');
    if (around && divider && !win.failed) {
      divider.scrollIntoView({ block: 'start' });
      measure();
    } else {
      scrollToBottom();
    }
  }

  $effect(() => {
    const id = channelId;
    untrack(() => void start(id));
  });

  // A search result or pin in the channel that is already open.
  $effect(() => {
    const j = ui.jump;
    if (j && j.channelId === channelId && win.channelId === channelId && win.loading === 'idle') {
      untrack(() => void start(channelId));
    }
  });

  /** Runs a load and keeps the row at the top of the viewport where it was. */
  async function anchored(load: () => Promise<unknown>) {
    const box = scroller.getBoundingClientRect().top;
    const el = [...scroller.querySelectorAll<HTMLElement>('[data-row]')].find(
      (e) => e.getBoundingClientRect().bottom > box,
    );
    const key = el?.dataset['row'];
    const offset = el ? el.getBoundingClientRect().top - box : 0;
    await load();
    await tick();
    if (!key) return;
    const again = scroller.querySelector<HTMLElement>(`[data-row="${CSS.escape(key)}"]`);
    if (again) scroller.scrollTop += again.getBoundingClientRect().top - box - offset;
  }

  function onscroll() {
    measure();
    if (win.loading !== 'idle') return;
    if (scroller.scrollTop < NEAR_PX && win.hasBefore) {
      void anchored(() => win.loadOlder());
    } else if (
      scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < NEAR_PX &&
      win.hasAfter
    ) {
      void anchored(() => win.loadNewer());
    }
  }

  async function jumpToLatest() {
    if (win.hasAfter) {
      await win.open(channelId, null);
      await tick();
    }
    scrollToBottom();
    scroller.focus();
  }

  // Stay pinned to the bottom as messages arrive, and keep the node count capped.
  $effect(() => {
    void lastId;
    void queued.length;
    if (!atBottom) return;
    untrack(() => win.enforceCap());
    void tick().then(() => atBottom && scrollToBottom());
  });

  // Read marking: visible, scrolled to the bottom, window focused.
  $effect(() => {
    const id = lastId;
    if (!id || !atBottom || win.hasAfter || !windowFocused) return;
    const stored = channels.memberships[channelId]?.last_read_id;
    if (lastMarked === id || (stored && stored >= id && unreadCount === 0)) return;
    const ch = channelId;
    const timer = setTimeout(() => {
      lastMarked = id;
      markRead(ch, id).catch(() => {
        lastMarked = '';
      });
    }, 400);
    return () => clearTimeout(timer);
  });

  onMount(() => {
    const sync = () =>
      (windowFocused = document.visibilityState === 'visible' && document.hasFocus());
    sync();
    window.addEventListener('focus', sync);
    window.addEventListener('blur', sync);
    document.addEventListener('visibilitychange', sync);
    return () => {
      window.removeEventListener('focus', sync);
      window.removeEventListener('blur', sync);
      document.removeEventListener('visibilitychange', sync);
      win.reset();
    };
  });
</script>

<div class="wrap">
  <div
    bind:this={scroller}
    class="scroller"
    tabindex="-1"
    data-region="messages"
    aria-label={t.chat.messageList}
    {onscroll}
  >
    {#if win.loading === 'older'}<p class="status" role="status">{t.chat.loadingOlder}</p>{/if}
    {#if win.failed}
      <p class="status" role="alert">
        {t.chat.loadFailed}
        <button class="btn" type="button" onclick={() => start(channelId)}>{t.chat.retry}</button>
      </p>
    {/if}
    <ul role="list">
      {#each rows as row (row.key)}
        <li data-row={row.key}>
          {#if row.type === 'day'}
            <DayDivider ts={row.ts} />
          {:else if row.type === 'new'}
            <NewDivider />
          {:else}
            <MessageItem message={row.message} grouped={row.grouped} />
          {/if}
        </li>
      {/each}
      {#each queued as item (item.nonce)}
        <li data-row={`pending:${item.nonce}`}>
          <MessageItem message={pendingMessage(item, session.me)} pending={item} />
        </li>
      {/each}
    </ul>
    <EphemeralNotice {channelId} />
    {#if win.loading === 'newer'}<p class="status" role="status">{t.chat.loadingNewer}</p>{/if}
  </div>
  {#if showJump}
    <button class="btn jump" type="button" onclick={jumpToLatest}>
      <ArrowDown aria-hidden="true" size={16} />
      {unreadCount > 0
        ? fmt(t.chat.jumpToLatestN, { n: String(unreadCount) })
        : t.chat.jumpToLatest}
    </button>
  {/if}
</div>

<style>
  .wrap {
    position: relative;
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
  .scroller {
    flex: 1;
    overflow-y: auto;
    overflow-anchor: none;
    padding: var(--s2) 0;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    content-visibility: auto;
    contain-intrinsic-size: auto 56px;
  }
  .status {
    text-align: center;
    color: var(--fg-muted);
    margin: var(--s2);
  }
  .jump {
    position: absolute;
    bottom: var(--s3);
    left: 50%;
    transform: translateX(-50%);
    display: inline-flex;
    align-items: center;
    gap: var(--s2);
    background: var(--accent);
    color: var(--accent-fg);
    border-color: var(--accent);
    box-shadow: 0 2px 8px var(--shadow);
  }
</style>
