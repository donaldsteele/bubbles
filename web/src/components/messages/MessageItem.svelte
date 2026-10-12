<script lang="ts">
  import { MessageSquare, Pencil, Pin, PinOff, SmilePlus, Trash2 } from '@lucide/svelte';
  import type { Message } from '../../lib/api/types';
  import { outbox, type OutboxItem } from '../../lib/composer/outbox.svelte';
  import { fmt, t } from '../../lib/i18n/en';
  import {
    deleteMessage,
    editMessage,
    senderName,
    toggleReaction,
  } from '../../lib/message-actions';
  import { clockTime, relTime } from '../../lib/messages/time';
  import { navigate } from '../../lib/router';
  import { channels } from '../../lib/state/channels.svelte';
  import { session } from '../../lib/state/session.svelte';
  import { ui } from '../../lib/state/ui.svelte';
  import { pinMessage, unpinMessage } from '../../lib/pins';
  import { messages } from '../../lib/state/messages.svelte';
  import AlertCard from '../alerts/AlertCard.svelte';
  import Dialog from '../a11y/Dialog.svelte';
  import EmojiPicker from '../emoji/EmojiPicker.svelte';
  import Attachments from '../files/Attachments.svelte';
  import Markdown from './Markdown.svelte';
  import Reactions from './Reactions.svelte';

  interface Props {
    message: Message;
    grouped?: boolean;
    /** Set for optimistic messages that are not on the server yet. */
    pending?: OutboxItem;
    /** Inside the thread drawer: no reply button or summary. */
    inThread?: boolean;
  }

  let { message, grouped = false, pending, inThread = false }: Props = $props();

  const who = $derived(senderName(message));
  const mine = $derived(message.author.id === session.me?.id);
  const canEdit = $derived(mine && !message.deleted && message.kind === 'text' && !pending);
  const canDelete = $derived(
    !pending &&
      !message.deleted &&
      message.kind !== 'system' &&
      (mine || session.isAdmin || channels.memberships[message.channel_id]?.role === 'owner'),
  );
  const editing = $derived(ui.editingId === message.id);

  let draft = $state('');
  let saving = $state(false);
  let editError = $state('');
  let confirming = $state(false);
  let reacting = $state(false);
  const pinned = $derived(messages.pins[message.channel_id]?.includes(message.id) ?? false);

  async function togglePin() {
    try {
      if (pinned) await unpinMessage(message.channel_id, message.id);
      else await pinMessage(message.channel_id, message.id);
    } catch {
      // the pin state is refreshed by the next pin event
    }
  }

  async function react(emoji: string) {
    reacting = false;
    try {
      await toggleReaction(message, emoji, session.me?.id ?? '');
    } catch {
      // the reaction bar keeps its previous state
    }
  }

  $effect(() => {
    if (editing) {
      draft = message.body;
      editError = '';
    }
  });

  function focusOnMount(node: HTMLTextAreaElement) {
    node.focus();
    node.setSelectionRange(node.value.length, node.value.length);
  }

  async function save() {
    const body = draft.trim();
    if (body === '' || saving) return;
    if (body === message.body) {
      ui.endEdit();
      return;
    }
    saving = true;
    try {
      await editMessage(message.id, body);
      ui.endEdit();
    } catch {
      editError = t.common.genericError;
    } finally {
      saving = false;
    }
  }

  function editKey(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      ui.endEdit();
    } else if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      void save();
    }
  }

  async function confirmDelete() {
    confirming = false;
    try {
      await deleteMessage(message);
    } catch {
      // the message stays; the user can retry
    }
  }

  function openThread(e: MouseEvent) {
    ui.drawerTrigger = e.currentTarget as HTMLElement;
    navigate(`/c/${message.channel_id}/t/${message.id}`);
  }

  const label = $derived(
    fmt(t.chat.messageBy, { name: who.name, time: clockTime(message.created_at) }),
  );
</script>

<!-- Focusable so keyboard users can reach the hover-only actions (focus-within shows them). -->
<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
<article
  tabindex="0"
  class="msg"
  class:grouped
  class:pending={pending && pending.status !== 'failed'}
  class:failed={pending?.status === 'failed'}
  aria-label={label}
  data-id={message.id}
>
  {#if !grouped}
    <div class="avatar" aria-hidden="true">
      {#if message.author.avatar_url && !who.webhook}
        <img src={message.author.avatar_url} alt="" width="36" height="36" />
      {:else}
        {who.name.charAt(0).toUpperCase()}
      {/if}
    </div>
  {:else}
    <div class="avatar gap"></div>
  {/if}
  <div class="body">
    {#if !grouped}
      <div class="meta">
        <strong class="author">{who.name}</strong>
        {#if who.webhook}<span class="chip">{t.chat.webhook}</span>{/if}
        <time datetime={message.created_at}>{clockTime(message.created_at)}</time>
      </div>
    {/if}

    {#if message.deleted}
      <p class="tombstone"><em>{t.chat.deleted}</em></p>
    {:else if editing}
      <div class="edit">
        <label class="visually-hidden" for={`edit-${message.id}`}>{t.chat.edit}</label>
        <textarea
          id={`edit-${message.id}`}
          rows="3"
          bind:value={draft}
          onkeydown={editKey}
          use:focusOnMount></textarea>
        {#if editError}<p class="form-error" role="alert">{editError}</p>{/if}
        <div class="row">
          <button class="btn primary" type="button" disabled={saving} onclick={save}
            >{t.chat.save}</button
          >
          <button class="btn" type="button" onclick={() => ui.endEdit()}>{t.chat.cancel}</button>
        </div>
      </div>
    {:else if message.kind === 'alert' && message.alert}
      <AlertCard {message} {inThread} />
    {:else}
      {#if message.body}<Markdown source={message.body} />{/if}
      {#if message.edited_at}<span class="edited">{t.chat.edited}</span>{/if}
    {/if}

    {#if !message.deleted && !editing}
      {#if message.files.length > 0}<Attachments files={message.files} />{/if}
      <Reactions {message} />
    {/if}

    {#if pending}
      {#if pending.status === 'failed'}
        <p class="state" role="status">
          {t.chat.sendFailed}
          <button class="link" type="button" onclick={() => outbox.retry(pending.nonce)}
            >{t.chat.retry}</button
          >
          <button class="link" type="button" onclick={() => outbox.remove(pending.nonce)}
            >{t.chat.discard}</button
          >
        </p>
      {:else}
        <p class="state">{t.chat.sending}</p>
      {/if}
    {/if}

    {#if !inThread && message.reply_count > 0 && !message.deleted}
      <a class="summary" href={`/c/${message.channel_id}/t/${message.id}`}>
        <MessageSquare aria-hidden="true" size={14} />
        {fmt(message.reply_count === 1 ? t.chat.replyOne : t.chat.replies, {
          n: String(message.reply_count),
          when: message.last_reply_at ? relTime(message.last_reply_at) : '',
        })}
      </a>
    {/if}
  </div>

  {#if !pending && !editing && !message.deleted}
    <div class="actions">
      {#if !inThread}
        <button
          class="icon"
          type="button"
          aria-label={t.chat.reply}
          title={t.chat.reply}
          onclick={openThread}
        >
          <MessageSquare aria-hidden="true" size={16} />
        </button>
      {/if}
      <button
        class="icon"
        type="button"
        aria-label={t.rich.addReaction}
        title={t.rich.addReaction}
        onclick={() => (reacting = true)}
      >
        <SmilePlus aria-hidden="true" size={16} />
      </button>
      <button
        class="icon"
        type="button"
        aria-label={pinned ? t.rich.unpin : t.rich.pin}
        title={pinned ? t.rich.unpin : t.rich.pin}
        aria-pressed={pinned}
        onclick={togglePin}
      >
        {#if pinned}<PinOff aria-hidden="true" size={16} />{:else}<Pin
            aria-hidden="true"
            size={16}
          />{/if}
      </button>
      {#if canEdit}
        <button
          class="icon"
          type="button"
          aria-label={t.chat.edit}
          title={t.chat.edit}
          onclick={() => ui.startEdit(message.id)}
        >
          <Pencil aria-hidden="true" size={16} />
        </button>
      {/if}
      {#if canDelete}
        <button
          class="icon"
          type="button"
          aria-label={t.chat.delete}
          title={t.chat.delete}
          onclick={() => {
            confirming = true;
          }}
        >
          <Trash2 aria-hidden="true" size={16} />
        </button>
      {/if}
    </div>
  {/if}
</article>

{#if reacting}
  <EmojiPicker onclose={() => (reacting = false)} onpick={react} />
{/if}

{#if confirming}
  <Dialog title={t.chat.deleteTitle} alert onclose={() => (confirming = false)}>
    <p>{t.chat.deleteBody}</p>
    <div class="row">
      <button class="btn" type="button" data-autofocus onclick={() => (confirming = false)}
        >{t.chat.cancel}</button
      >
      <button class="btn danger" type="button" onclick={confirmDelete}>
        <Trash2 aria-hidden="true" size={16} />
        {t.chat.delete}
      </button>
    </div>
  </Dialog>
{/if}

<style>
  .msg {
    position: relative;
    display: flex;
    gap: var(--s3);
    padding: var(--s1) var(--s4);
  }
  .msg:hover,
  .msg:focus-within {
    background: var(--bg-elev);
  }
  .msg.pending {
    opacity: 0.7;
  }
  .msg.failed {
    box-shadow: inset 3px 0 0 var(--bad);
  }
  .avatar {
    flex: none;
    width: 36px;
    height: 36px;
    border-radius: var(--radius);
    background: var(--bg-elev2);
    border: 1px solid var(--line);
    display: grid;
    place-items: center;
    font-weight: 700;
    overflow: hidden;
  }
  .avatar.gap {
    visibility: hidden;
    height: 1px;
  }
  .body {
    flex: 1;
    min-width: 0;
  }
  .meta {
    display: flex;
    align-items: baseline;
    gap: var(--s2);
  }
  time,
  .edited,
  .state {
    color: var(--fg-muted);
    font-size: 12px;
  }
  .state {
    margin: 0;
  }
  .chip {
    border: 1px solid var(--line-strong);
    border-radius: 999px;
    padding: 0 var(--s2);
    font-size: 11px;
    text-transform: uppercase;
  }
  .tombstone {
    margin: 0;
    color: var(--fg-muted);
  }
  .actions {
    position: absolute;
    top: 2px;
    right: var(--s4);
    display: none;
    gap: 2px;
    background: var(--bg-elev);
    border: 1px solid var(--line-strong);
    border-radius: var(--radius);
  }
  .msg:hover .actions,
  .msg:focus-within .actions {
    display: flex;
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
  .icon:hover {
    background: var(--bg-elev2);
  }
  .summary {
    display: inline-flex;
    align-items: center;
    gap: var(--s1);
    font-size: 13px;
    min-height: 24px;
  }
  .link {
    background: none;
    border: 0;
    color: var(--link);
    text-decoration: underline;
    cursor: pointer;
    min-height: 24px;
  }
  .edit textarea {
    width: 100%;
    padding: var(--s2);
    background: var(--bg-elev2);
    border: 1px solid var(--line-strong);
    border-radius: var(--radius);
    resize: vertical;
  }
  .row {
    display: flex;
    gap: var(--s2);
    margin-top: var(--s2);
  }
  .btn.danger {
    background: var(--badbg);
    border-color: var(--bad);
    display: inline-flex;
    align-items: center;
    gap: var(--s1);
  }
  /* Touch screens have no hover: the actions sit in a row under the message. */
  @media (hover: none) {
    .msg {
      flex-wrap: wrap;
    }
    .actions {
      display: flex;
      position: static;
      flex-basis: 100%;
      justify-content: flex-end;
      border: 0;
      background: none;
    }
  }
</style>
