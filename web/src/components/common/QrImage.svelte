<script lang="ts">
  // Draws `value` as a QR code in the browser. The text never leaves the page
  // (invite links and connection strings are secrets). The encoder is a lazy
  // chunk, so it is not part of the first-paint bundle.
  import { onMount } from 'svelte';
  import { t } from '../../lib/i18n/en';

  let {
    value,
    label = t.admin.qrLabel,
    size = 192,
  }: { value: string; label?: string; size?: number } = $props();

  const BORDER = 2;

  let path = $state('');
  let dim = $state(0);
  let failed = $state(false);
  let mounted = false;

  async function render(text: string) {
    try {
      const { encode } = await import('uqr');
      if (!mounted) return;
      const { data } = encode(text, { ecc: 'M', border: 0 });
      const parts: string[] = [];
      data.forEach((row, y) => {
        row.forEach((dark, x) => {
          if (dark) parts.push(`M${x + BORDER} ${y + BORDER}h1v1h-1z`);
        });
      });
      path = parts.join('');
      dim = data.length + BORDER * 2;
      failed = false;
    } catch {
      path = '';
      failed = true;
    }
  }

  onMount(() => {
    mounted = true;
    return () => (mounted = false);
  });

  $effect(() => {
    void render(value);
  });
</script>

{#if failed}
  <p class="form-error" role="alert">{t.admin.qrFailed}</p>
{:else}
  <svg
    class="qr"
    role="img"
    aria-label={label}
    width={size}
    height={size}
    viewBox={`0 0 ${dim || 1} ${dim || 1}`}
    shape-rendering="crispEdges"
  >
    <rect width="100%" height="100%" fill="#fff" />
    <path d={path} fill="#000" />
  </svg>
{/if}

<style>
  .qr {
    display: block;
    border-radius: var(--radius);
  }
</style>
