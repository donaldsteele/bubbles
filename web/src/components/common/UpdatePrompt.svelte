<script lang="ts">
  import { RefreshCw } from '@lucide/svelte';
  import { t } from '../../lib/i18n/en';
  import { pwaUpdate } from '../../lib/pwa/update.svelte';
</script>

{#if pwaUpdate.state !== 'idle'}
  <div class="update" role="status">
    <RefreshCw aria-hidden="true" size={18} />
    <span>{t.pwa.updateAvailable}</span>
    <button
      class="btn primary"
      type="button"
      disabled={pwaUpdate.state !== 'available'}
      onclick={() => pwaUpdate.apply()}
    >
      {t.pwa.reload}
    </button>
  </div>
{/if}

<style>
  .update {
    position: fixed;
    right: max(var(--s4), env(safe-area-inset-right));
    bottom: max(var(--s4), env(safe-area-inset-bottom));
    z-index: 50;
    display: flex;
    align-items: center;
    gap: var(--s3);
    padding: var(--s2) var(--s3);
    background: var(--bg-elev2);
    border: 1px solid var(--line-strong);
    border-radius: var(--radius);
    box-shadow: 0 2px 8px var(--shadow);
  }
</style>
