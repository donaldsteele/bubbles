<script lang="ts">
  import { ChevronLeft, ChevronRight, Download, X } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import type { Schemas } from '../../lib/api/client';
  import { downloadUrl } from '../../lib/files/format';
  import { fmt, t } from '../../lib/i18n/en';

  interface Props {
    images: Schemas['File'][];
    index: number;
    onclose: () => void;
  }

  let { images, index = $bindable(), onclose }: Props = $props();

  let box: HTMLDivElement | undefined = $state();
  const current = $derived(images[index]);

  onMount(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    box?.querySelector<HTMLElement>('[data-autofocus]')?.focus();
    return () => {
      if (opener?.isConnected) opener.focus();
    };
  });

  function step(d: 1 | -1) {
    index = (index + d + images.length) % images.length;
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      onclose();
    } else if (e.key === 'ArrowLeft' && images.length > 1) {
      e.preventDefault();
      step(-1);
    } else if (e.key === 'ArrowRight' && images.length > 1) {
      e.preventDefault();
      step(1);
    } else if (e.key === 'Tab') {
      const f = [...(box?.querySelectorAll<HTMLElement>('a[href],button:not([disabled])') ?? [])];
      if (f.length === 0) return;
      const first = f[0] as HTMLElement;
      const last = f[f.length - 1] as HTMLElement;
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }
  }
</script>

{#if current}
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div class="backdrop" onclick={(e) => e.target === e.currentTarget && onclose()}>
    <div
      bind:this={box}
      class="box"
      role="dialog"
      tabindex="-1"
      aria-modal="true"
      aria-label={t.rich.lightbox}
      {onkeydown}
    >
      <div class="bar">
        <span class="count" aria-live="polite"
          >{fmt(t.rich.imageOf, { n: String(index + 1), total: String(images.length) })}</span
        >
        <a
          class="btn"
          href={downloadUrl(current.url)}
          download={current.name}
          aria-label={fmt(t.rich.downloadFile, { name: current.name })}
          title={t.rich.download}
        >
          <Download aria-hidden="true" size={18} />
        </a>
        <button
          class="btn"
          type="button"
          data-autofocus
          aria-label={t.rich.close}
          title={t.rich.close}
          onclick={onclose}
        >
          <X aria-hidden="true" size={18} />
        </button>
      </div>
      <div class="stage">
        {#if images.length > 1}
          <button
            class="btn nav"
            type="button"
            aria-label={t.rich.prevImage}
            title={t.rich.prevImage}
            onclick={() => step(-1)}
          >
            <ChevronLeft aria-hidden="true" size={22} />
          </button>
        {/if}
        <img src={current.url} alt={fmt(t.rich.image, { name: current.name })} />
        {#if images.length > 1}
          <button
            class="btn nav"
            type="button"
            aria-label={t.rich.nextImage}
            title={t.rich.nextImage}
            onclick={() => step(1)}
          >
            <ChevronRight aria-hidden="true" size={22} />
          </button>
        {/if}
      </div>
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 200;
    display: grid;
    place-items: center;
    background: var(--backdrop);
  }
  .box {
    display: flex;
    flex-direction: column;
    gap: var(--s2);
    max-width: 96vw;
    max-height: 96vh;
  }
  .bar {
    display: flex;
    justify-content: flex-end;
    align-items: center;
    gap: var(--s2);
  }
  .count {
    margin-right: auto;
    color: var(--fg);
  }
  .stage {
    display: flex;
    align-items: center;
    gap: var(--s2);
    min-height: 0;
  }
  img {
    max-width: 80vw;
    max-height: 80vh;
    object-fit: contain;
    background: var(--bg-elev);
  }
  .btn {
    display: inline-grid;
    place-items: center;
    min-width: 44px;
    min-height: 44px;
  }
</style>
