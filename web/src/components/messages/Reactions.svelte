<script lang="ts">
  import { SmilePlus } from '@lucide/svelte';
  import type { Message } from '../../lib/api/types';
  import { fmt, t } from '../../lib/i18n/en';
  import { toggleReaction } from '../../lib/message-actions';
  import { session } from '../../lib/state/session.svelte';
  import { users } from '../../lib/state/users.svelte';
  import EmojiPicker from '../emoji/EmojiPicker.svelte';

  let { message }: { message: Message } = $props();

  const meId = $derived(session.me?.id ?? '');
  let picking = $state(false);
  let failed = $state(false);

  function names(ids: string[]): string {
    return ids
      .map((id) => (id === meId ? t.rich.you : (users.get(id)?.display_name ?? '')))
      .filter(Boolean)
      .join(', ');
  }

  async function toggle(emoji: string) {
    failed = false;
    try {
      await toggleReaction(message, emoji, meId);
    } catch {
      failed = true;
    }
  }
</script>

{#if message.reactions.length > 0}
  <div class="reactions" role="group" aria-label={t.rich.addReaction}>
    {#each message.reactions as r (r.emoji)}
      {@const mine = r.user_ids.includes(meId)}
      <button
        type="button"
        class="chip"
        class:mine
        aria-pressed={mine}
        aria-label={fmt(t.rich.reactionToggle, { emoji: r.emoji, n: String(r.count) })}
        title={fmt(t.rich.reactionBy, { emoji: r.emoji, names: names(r.user_ids) })}
        onclick={() => toggle(r.emoji)}
      >
        <span aria-hidden="true">{r.emoji}</span>
        <span class="n" aria-hidden="true">{r.count}</span>
      </button>
    {/each}
    <button
      type="button"
      class="chip add"
      aria-label={t.rich.addReaction}
      title={t.rich.addReaction}
      onclick={() => (picking = true)}
    >
      <SmilePlus aria-hidden="true" size={14} />
    </button>
  </div>
  {#if failed}<p class="form-error" role="alert">{t.common.genericError}</p>{/if}
{/if}

{#if picking}
  <EmojiPicker
    onclose={() => (picking = false)}
    onpick={(e) => {
      picking = false;
      void toggle(e);
    }}
  />
{/if}

<style>
  .reactions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--s1);
    margin-top: var(--s1);
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: var(--s1);
    min-height: 26px;
    padding: 0 var(--s2);
    background: var(--bg-elev2);
    border: 1px solid var(--line-strong);
    border-radius: 999px;
    cursor: pointer;
  }
  .chip.mine {
    border-color: var(--accent);
    box-shadow: inset 0 0 0 1px var(--accent);
  }
  .n {
    font-size: 12px;
    font-variant-numeric: tabular-nums;
  }
</style>
