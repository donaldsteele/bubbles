<script lang="ts">
  import type { Snippet } from 'svelte';
  import { onMount } from 'svelte';
  import { portal } from '../../lib/a11y/portal';
  import { t } from '../../lib/i18n/en';

  interface Props {
    title: string;
    onclose: () => void;
    children: Snippet;
    /** Use role="alertdialog" for destructive confirmations. */
    alert?: boolean;
  }

  let { title, onclose, children, alert = false }: Props = $props();

  const uid = $props.id();
  const titleId = `dlg-${uid}-title`;
  let box: HTMLDivElement;

  const FOCUSABLE =
    'a[href],button:not([disabled]),textarea:not([disabled]),input:not([disabled]),select:not([disabled]),[tabindex]:not([tabindex="-1"])';

  function focusables(): HTMLElement[] {
    return [...box.querySelectorAll<HTMLElement>(FOCUSABLE)].filter((e) => !e.hidden);
  }

  onMount(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const first = box.querySelector<HTMLElement>('[data-autofocus]') ?? focusables()[0] ?? box;
    first.focus();
    return () => {
      if (opener?.isConnected) opener.focus();
    };
  });

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      onclose();
      return;
    }
    if (e.key !== 'Tab') return;
    const list = focusables();
    if (list.length === 0) {
      e.preventDefault();
      return;
    }
    const first = list[0] as HTMLElement;
    const last = list[list.length - 1] as HTMLElement;
    const active = document.activeElement;
    if (e.shiftKey && (active === first || active === box)) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && active === last) {
      e.preventDefault();
      first.focus();
    }
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div use:portal class="backdrop" onclick={(e) => e.target === e.currentTarget && onclose()}>
  <div
    bind:this={box}
    class="dialog"
    role={alert ? 'alertdialog' : 'dialog'}
    aria-modal="true"
    aria-labelledby={titleId}
    tabindex="-1"
    {onkeydown}
  >
    <div class="head">
      <h2 id={titleId}>{title}</h2>
      <button class="btn close" type="button" aria-label={t.chat.close} onclick={onclose}>
        <span aria-hidden="true">×</span>
      </button>
    </div>
    {@render children()}
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 100;
    display: grid;
    place-items: center;
    background: var(--backdrop);
    padding: var(--s4);
  }
  .dialog {
    width: min(480px, 100%);
    max-height: 90vh;
    overflow-y: auto;
    background: var(--bg-elev);
    border: 1px solid var(--line-strong);
    border-radius: var(--radius);
    box-shadow: 0 8px 32px var(--shadow);
    padding: var(--s4);
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--s3);
  }
  h2 {
    margin: 0;
    font-size: 18px;
  }
  .close {
    min-width: 44px;
  }
</style>
