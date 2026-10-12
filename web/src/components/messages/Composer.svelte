<script lang="ts">
  import { onMount } from 'svelte';
  import { Paperclip, SendHorizontal, Smile } from '@lucide/svelte';
  import { api, call } from '../../lib/api/client';
  import {
    applySuggestion,
    findTrigger,
    listboxKey,
    suggestionsFor,
    type Trigger,
  } from '../../lib/composer/autocomplete';
  import { parseCommand } from '../../lib/composer/commands';
  import { loadEmoji, type EmojiData } from '../../lib/emoji/data';
  import { UploadQueue } from '../../lib/files/upload.svelte';
  import { channels } from '../../lib/state/channels.svelte';
  import { commands } from '../../lib/state/commands.svelte';
  import { users } from '../../lib/state/users.svelte';
  import Autocomplete from '../composer/Autocomplete.svelte';
  import EmojiPicker from '../emoji/EmojiPicker.svelte';
  import UploadList from '../files/UploadQueue.svelte';
  import { clearDraft, loadDraft, saveDraft } from '../../lib/composer/drafts';
  import { markerFor, wrapSelection } from '../../lib/composer/format';
  import { outbox } from '../../lib/composer/outbox.svelte';
  import { onLocalReset } from '../../lib/reset';
  import { fmt, t } from '../../lib/i18n/en';
  import { messages } from '../../lib/state/messages.svelte';
  import { session } from '../../lib/state/session.svelte';
  import { settings } from '../../lib/state/settings.svelte';
  import { ui } from '../../lib/state/ui.svelte';
  import { getClient } from '../../lib/ws/realtime';

  interface Props {
    channelId: string;
    channelName: string;
    /** Set inside the thread drawer: replies go to this root. */
    threadRootId?: string;
    /** data-region value so F6 can find this composer. */
    region?: string;
  }

  let { channelId, channelName, threadRootId, region = 'composer' }: Props = $props();

  let text = $state('');
  let box: HTMLTextAreaElement;
  let fileInput: HTMLInputElement;
  let loadedKey = '';
  let trigger = $state<Trigger | null>(null);
  let activeIdx = $state(0);
  let dismissed = $state(false);
  let emojiData = $state<EmojiData | null>(null);
  let pickingEmoji = $state(false);
  let dragging = $state(false);
  let dragDepth = 0;
  let sendError = $state('');
  let sending = $state(false);

  const queue = new UploadQueue(() => settings.public?.upload_max_bytes ?? 0);
  const listId = $derived(`ac-${region}`);
  const suggestions = $derived(
    trigger && !dismissed
      ? suggestionsFor(trigger, {
          users: Object.values(users.byId),
          channels: channels.joined,
          commands: commands.items,
          emoji: emojiData,
        })
      : [],
  );
  const open = $derived(suggestions.length > 0);

  // Load the draft whenever the scope changes; flush the previous one first.
  $effect(() => {
    const key = `${channelId}:${threadRootId ?? ''}`;
    if (key === loadedKey) return;
    loadedKey = key;
    text = loadDraft(channelId, threadRootId);
    queue.reset();
    trigger = null;
    sendError = '';
  });

  // A content reset deletes stored drafts; the visible text goes with them.
  onMount(() => {
    void commands.load();
    const off = onLocalReset(() => {
      text = '';
      queue.reset();
    });
    return () => {
      off();
      queue.reset();
    };
  });

  function persist() {
    saveDraft(channelId, text, threadRootId);
  }

  function updateTrigger() {
    trigger = findTrigger(text, box.selectionStart);
    activeIdx = 0;
    dismissed = false;
    if (trigger?.kind === 'emoji' && !emojiData) {
      loadEmoji()
        .then((d) => {
          emojiData = d;
        })
        .catch(() => {});
    } else if (trigger?.kind === 'command') {
      void commands.load();
    }
  }

  function setText(value: string, caret: number) {
    text = value;
    persist();
    queueMicrotask(() => {
      box.focus();
      box.setSelectionRange(caret, caret);
    });
  }

  function pick(i: number) {
    const s = suggestions[i];
    if (!trigger || !s) return;
    const r = applySuggestion(text, trigger, s.insert);
    trigger = null;
    setText(r.value, r.caret);
  }

  function insertEmoji(emoji: string) {
    pickingEmoji = false;
    const at = box.selectionStart;
    setText(text.slice(0, at) + emoji + text.slice(box.selectionEnd), at + emoji.length);
  }

  function oninput() {
    updateTrigger();
    persist();
    if (text.trim() !== '') getClient()?.sendTyping(channelId);
  }

  async function runCommand(name: string, args: string) {
    sending = true;
    try {
      await call(
        api.POST('/commands/invoke', {
          body: {
            channel_id: channelId,
            command: name,
            ...(args ? { args } : {}),
            ...(threadRootId ? { thread_root_id: threadRootId } : {}),
          },
        }),
      );
      text = '';
      clearDraft(channelId, threadRootId);
    } catch {
      sendError = t.rich.commandFailed;
    } finally {
      sending = false;
    }
  }

  function send() {
    const body = text.trim();
    const fileIds = queue.done.map((f) => f.id);
    if ((body === '' && fileIds.length === 0) || queue.busy || sending) return;
    sendError = '';
    trigger = null;
    const cmd = fileIds.length === 0 ? parseCommand(body, commands.names) : null;
    if (cmd) {
      void runCommand(cmd.name, cmd.args);
      return;
    }
    const item = outbox.enqueue(channelId, body, threadRootId ?? null, undefined, fileIds);
    text = '';
    clearDraft(channelId, threadRootId);
    queue.clearFinished();
    void outbox.deliver(item.nonce);
  }

  function onpaste(e: ClipboardEvent) {
    const files = e.clipboardData?.files;
    if (files && files.length > 0) {
      e.preventDefault();
      queue.add(Array.from(files));
    }
  }

  const hasFiles = (e: DragEvent) => e.dataTransfer?.types.includes('Files') ?? false;

  function ondragenter(e: DragEvent) {
    if (!hasFiles(e)) return;
    e.preventDefault();
    dragDepth += 1;
    dragging = true;
  }

  function ondragover(e: DragEvent) {
    if (hasFiles(e)) e.preventDefault();
  }

  function ondragleave() {
    dragDepth = Math.max(0, dragDepth - 1);
    if (dragDepth === 0) dragging = false;
  }

  function ondrop(e: DragEvent) {
    if (!hasFiles(e)) return;
    e.preventDefault();
    dragDepth = 0;
    dragging = false;
    queue.add(Array.from(e.dataTransfer?.files ?? []));
  }

  function onpick(e: Event) {
    const input = e.currentTarget as HTMLInputElement;
    queue.add(Array.from(input.files ?? []));
    input.value = '';
  }

  function lastOwn() {
    const meId = session.me?.id;
    const list = threadRootId ? messages.thread(threadRootId) : messages.list(channelId);
    for (let i = list.length - 1; i >= 0; i--) {
      const m = list[i];
      if (m && !m.deleted && m.kind === 'text' && m.author.id === meId) return m;
    }
    return undefined;
  }

  function onkeydown(e: KeyboardEvent) {
    if (open && !e.isComposing && !e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey) {
      const r = listboxKey(e.key, activeIdx, suggestions.length);
      if (r.action === 'move') {
        e.preventDefault();
        activeIdx = r.active;
        return;
      }
      if (r.action === 'accept') {
        e.preventDefault();
        pick(r.active);
        return;
      }
      if (r.action === 'close') {
        e.preventDefault();
        e.stopPropagation();
        dismissed = true;
        return;
      }
    }
    if ((e.ctrlKey || e.metaKey) && !e.altKey) {
      const marker = markerFor(e.key);
      if (marker) {
        e.preventDefault();
        const r = wrapSelection(text, box.selectionStart, box.selectionEnd, marker);
        text = r.value;
        queueMicrotask(() => box.setSelectionRange(r.start, r.end));
        persist();
        return;
      }
    }
    if (e.key === 'Enter' && !e.isComposing) {
      const sendKey = settings.enterSends ? !e.shiftKey : e.shiftKey;
      if (sendKey && !e.ctrlKey && !e.metaKey) {
        e.preventDefault();
        send();
      }
      return;
    }
    if (e.key === 'ArrowUp' && text === '' && !e.shiftKey && !e.altKey) {
      const m = lastOwn();
      if (m) {
        e.preventDefault();
        ui.startEdit(m.id);
      }
    }
  }
</script>

<div class="wrap" role="presentation" {ondragenter} {ondragover} {ondragleave} {ondrop}>
  {#if dragging}
    <div class="drop" aria-hidden="true">{t.rich.dropFiles}</div>
  {/if}
  {#if open}
    <Autocomplete id={listId} items={suggestions} active={activeIdx} onpick={pick} />
  {/if}
  <UploadList {queue} />
  {#if sendError}<p class="form-error err" role="alert">{sendError}</p>{/if}
  <form
    class="composer"
    onsubmit={(e) => {
      e.preventDefault();
      send();
    }}
  >
    <label class="visually-hidden" for={`composer-${region}`}>
      {fmt(t.chat.composerLabel, { channel: channelName })}
    </label>
    <textarea
      id={`composer-${region}`}
      bind:this={box}
      bind:value={text}
      data-region={region}
      rows="2"
      role="combobox"
      aria-autocomplete="list"
      aria-expanded={open}
      aria-controls={listId}
      aria-activedescendant={open ? `${listId}-opt-${activeIdx}` : undefined}
      placeholder={threadRootId ? t.chat.replyPlaceholder : t.chat.composer}
      {oninput}
      {onkeydown}
      {onpaste}
      onclick={updateTrigger}
      onblur={() => (trigger = null)}></textarea>
    <input bind:this={fileInput} type="file" multiple hidden onchange={onpick} />
    <button
      class="btn tool"
      type="button"
      aria-label={t.rich.attach}
      title={t.rich.attach}
      onclick={() => fileInput.click()}
    >
      <Paperclip aria-hidden="true" size={18} />
    </button>
    <button
      class="btn tool"
      type="button"
      aria-label={t.rich.emojiPicker}
      title={t.rich.emojiPicker}
      onclick={() => (pickingEmoji = true)}
    >
      <Smile aria-hidden="true" size={18} />
    </button>
    <button
      class="btn primary send"
      type="submit"
      aria-label={t.chat.send}
      title={t.chat.send}
      disabled={queue.busy || sending}
    >
      <SendHorizontal aria-hidden="true" size={18} />
    </button>
  </form>
</div>

{#if pickingEmoji}
  <EmojiPicker onpick={insertEmoji} onclose={() => (pickingEmoji = false)} />
{/if}

<style>
  .wrap {
    position: relative;
  }
  .drop {
    position: absolute;
    inset: 0;
    z-index: 30;
    display: grid;
    place-items: center;
    background: var(--infobg);
    border: 2px dashed var(--accent);
    font-weight: 600;
  }
  .err {
    margin: var(--s1) var(--s4) 0;
  }
  .tool {
    min-width: 44px;
    padding: 0;
    display: grid;
    place-items: center;
  }
  .composer {
    display: flex;
    gap: var(--s2);
    padding: var(--s2) var(--s4) var(--s3);
    border-top: 1px solid var(--line);
  }
  textarea {
    flex: 1;
    min-height: 44px;
    max-height: 200px;
    padding: var(--s2) var(--s3);
    background: var(--bg-elev2);
    border: 1px solid var(--line-strong);
    border-radius: var(--radius);
    resize: vertical;
  }
  .send {
    min-width: 44px;
    padding: 0;
    display: grid;
    place-items: center;
  }
</style>
