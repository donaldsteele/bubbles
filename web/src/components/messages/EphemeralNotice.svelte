<script lang="ts">
  import { X } from '@lucide/svelte';
  import { t } from '../../lib/i18n/en';
  import { ephemerals } from '../../lib/state/ephemeral.svelte';
  import Markdown from './Markdown.svelte';

  let { channelId, threadRootId = null }: { channelId: string; threadRootId?: string | null } =
    $props();

  const notices = $derived(ephemerals.forScope(channelId, threadRootId));
</script>

{#each notices as n (n.key)}
  <div class="notice" role="status">
    <div class="body">
      <p class="only">{t.rich.onlyYou}</p>
      <Markdown source={n.body} />
    </div>
    <button
      class="icon"
      type="button"
      aria-label={t.rich.dismiss}
      title={t.rich.dismiss}
      onclick={() => ephemerals.dismiss(n.key)}
    >
      <X aria-hidden="true" size={14} />
    </button>
  </div>
{/each}

<style>
  .notice {
    display: flex;
    gap: var(--s2);
    margin: var(--s1) var(--s4);
    padding: var(--s2) var(--s3);
    background: var(--infobg);
    border: 1px dashed var(--line-strong);
    border-radius: var(--radius);
  }
  .body {
    flex: 1;
    min-width: 0;
  }
  .only {
    margin: 0;
    color: var(--fg-muted);
    font-size: 12px;
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
