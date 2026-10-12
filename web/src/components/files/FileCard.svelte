<script lang="ts">
  import { Download, File as FileIcon, FileAudio, FileVideo } from '@lucide/svelte';
  import type { Schemas } from '../../lib/api/client';
  import { downloadUrl, formatBytes } from '../../lib/files/format';
  import { fmt, t } from '../../lib/i18n/en';

  let { file }: { file: Schemas['File'] } = $props();

  const size = $derived(formatBytes(file.size));
</script>

<div class="card" role="group" aria-label={fmt(t.rich.fileCard, { name: file.name, size })}>
  {#if file.category === 'video'}
    <FileVideo aria-hidden="true" size={24} />
  {:else if file.category === 'audio'}
    <FileAudio aria-hidden="true" size={24} />
  {:else}
    <FileIcon aria-hidden="true" size={24} />
  {/if}
  <div class="info">
    <span class="name">{file.name}</span>
    <span class="size">{size}</span>
  </div>
  <a
    class="dl"
    href={downloadUrl(file.url)}
    download={file.name}
    aria-label={fmt(t.rich.downloadFile, { name: file.name })}
    title={t.rich.download}
  >
    <Download aria-hidden="true" size={18} />
  </a>
</div>

<style>
  .card {
    display: inline-flex;
    align-items: center;
    gap: var(--s3);
    max-width: 360px;
    padding: var(--s2) var(--s3);
    background: var(--bg-elev2);
    border: 1px solid var(--line-strong);
    border-radius: var(--radius);
  }
  .info {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 600;
  }
  .size {
    color: var(--fg-muted);
    font-size: 12px;
  }
  .dl {
    min-width: 32px;
    min-height: 32px;
    display: grid;
    place-items: center;
  }
</style>
