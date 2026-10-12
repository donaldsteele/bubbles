<script lang="ts">
  import { RotateCcw, X } from '@lucide/svelte';
  import { formatBytes } from '../../lib/files/format';
  import type { UploadItem, UploadQueue } from '../../lib/files/upload.svelte';
  import { fmt, t } from '../../lib/i18n/en';
  import { settings } from '../../lib/state/settings.svelte';

  let { queue }: { queue: UploadQueue } = $props();

  function errorText(item: UploadItem): string {
    switch (item.error) {
      case 'tooLarge':
        return fmt(t.rich.uploadTooLarge, {
          limit: formatBytes(settings.public?.upload_max_bytes ?? 0),
        });
      case 'network':
        return t.rich.uploadNetwork;
      case 'storage':
        return t.rich.uploadStorage;
      default:
        return t.rich.uploadServer;
    }
  }
</script>

{#if queue.items.length > 0}
  <ul class="uploads" aria-label={t.rich.uploads}>
    {#each queue.items as item (item.id)}
      <li class:bad={item.status === 'failed' || item.status === 'rejected'}>
        <span class="name">{item.name}</span>
        <span class="size">{formatBytes(item.size)}</span>
        {#if item.status === 'uploading' || item.status === 'queued'}
          <progress max="1" value={item.progress} aria-label={`${t.rich.uploading}: ${item.name}`}
          ></progress>
        {:else if item.status === 'done'}
          <span class="state">{t.rich.uploadDone}</span>
        {:else}
          <span class="state" role="status">{t.rich.uploadFailed}: {errorText(item)}</span>
        {/if}
        {#if item.status === 'failed'}
          <button
            class="icon"
            type="button"
            aria-label={fmt(t.rich.retryUpload, { name: item.name })}
            title={t.chat.retry}
            onclick={() => queue.retry(item.id)}
          >
            <RotateCcw aria-hidden="true" size={14} />
          </button>
        {/if}
        <button
          class="icon"
          type="button"
          aria-label={fmt(
            item.status === 'uploading' || item.status === 'queued'
              ? t.rich.cancelUpload
              : t.rich.removeUpload,
            { name: item.name },
          )}
          title={t.chat.cancel}
          onclick={() => queue.cancel(item.id)}
        >
          <X aria-hidden="true" size={14} />
        </button>
        {#if item.status === 'done' && item.file && item.file.category === 'image' && !item.file.meta_stripped}
          <p class="warn" role="note">{t.rich.metaNotStripped}</p>
        {/if}
      </li>
    {/each}
  </ul>
{/if}

<style>
  .uploads {
    list-style: none;
    margin: 0;
    padding: var(--s2) var(--s4) 0;
    display: flex;
    flex-direction: column;
    gap: var(--s1);
  }
  li {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--s2);
    padding: var(--s1) var(--s2);
    background: var(--bg-elev2);
    border: 1px solid var(--line);
    border-radius: var(--radius);
  }
  li.bad {
    border-color: var(--bad);
  }
  .name {
    max-width: 240px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .size,
  .state {
    color: var(--fg-muted);
    font-size: 12px;
  }
  progress {
    flex: 1;
    min-width: 80px;
  }
  .icon {
    min-width: 28px;
    min-height: 28px;
    display: grid;
    place-items: center;
    background: none;
    border: 0;
    cursor: pointer;
  }
  .warn {
    flex-basis: 100%;
    margin: 0;
    color: var(--warn);
    font-size: 12px;
  }
</style>
