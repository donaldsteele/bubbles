<script lang="ts">
  import type { Schemas } from '../../lib/api/client';
  import { fmt, t } from '../../lib/i18n/en';
  import FileCard from './FileCard.svelte';
  import Lightbox from './Lightbox.svelte';

  let { files }: { files: Schemas['File'][] } = $props();

  type F = Schemas['File'];
  const images = $derived(files.filter((f) => f.category === 'image'));
  const media = $derived(files.filter((f) => f.category === 'video' || f.category === 'audio'));
  const others = $derived(files.filter((f) => f.category === 'file'));

  let open = $state(false);
  let lightboxIndex = $state(0);

  /** Thumbnail size that keeps the aspect ratio so the layout never shifts. */
  function dims(f: F): { width: number; height: number } {
    const w = f.width ?? 320;
    const h = f.height ?? 240;
    const scale = Math.min(1, 320 / Math.max(w, h));
    return {
      width: Math.max(1, Math.round(w * scale)),
      height: Math.max(1, Math.round(h * scale)),
    };
  }
</script>

{#if images.length > 0}
  <div class="grid">
    {#each images as f, i (f.id)}
      {@const d = dims(f)}
      <button
        type="button"
        class="thumb"
        aria-label={fmt(t.rich.image, { name: f.name })}
        onclick={() => {
          lightboxIndex = i;
          open = true;
        }}
      >
        <img
          src={f.thumb_url ?? f.url}
          alt=""
          loading="lazy"
          decoding="async"
          width={d.width}
          height={d.height}
        />
      </button>
      {#if !f.meta_stripped}
        <p class="warn" role="note">{t.rich.metaNotStripped}</p>
      {/if}
    {/each}
  </div>
{/if}

{#each media as f (f.id)}
  <div class="media">
    {#if f.category === 'video'}
      <!-- svelte-ignore a11y_media_has_caption -->
      <video controls preload="metadata" src={f.url} aria-label={f.name}></video>
    {:else}
      <audio controls preload="metadata" src={f.url} aria-label={f.name}></audio>
    {/if}
    <FileCard file={f} />
  </div>
{/each}

{#each others as f (f.id)}
  <div class="other"><FileCard file={f} /></div>
{/each}

{#if open}
  <Lightbox {images} bind:index={lightboxIndex} onclose={() => (open = false)} />
{/if}

<style>
  .grid {
    display: flex;
    flex-wrap: wrap;
    gap: var(--s2);
    margin-top: var(--s1);
    align-items: flex-start;
  }
  .thumb {
    padding: 0;
    border: 1px solid var(--line-strong);
    border-radius: var(--radius);
    background: var(--bg-elev2);
    cursor: zoom-in;
    line-height: 0;
    overflow: hidden;
    /* Tiny images still make a usable (24px) target. */
    min-width: 24px;
    min-height: 24px;
  }
  img {
    display: block;
    max-width: 100%;
    height: auto;
  }
  .media,
  .other {
    margin-top: var(--s2);
    display: flex;
    flex-direction: column;
    gap: var(--s1);
    align-items: flex-start;
  }
  video {
    max-width: min(480px, 100%);
    max-height: 320px;
  }
  audio {
    width: min(360px, 100%);
  }
  .warn {
    flex-basis: 100%;
    margin: 0;
    color: var(--warn);
    font-size: 12px;
  }
</style>
