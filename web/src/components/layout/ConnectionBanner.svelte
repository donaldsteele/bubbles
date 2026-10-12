<script lang="ts">
  import { WifiOff, RefreshCw, ServerCog } from '@lucide/svelte';
  import { retryServer } from '../../lib/boot';
  import { t } from '../../lib/i18n/en';
  import { connection } from '../../lib/state/connection.svelte';
  import { session } from '../../lib/state/session.svelte';

  // Most severe first. The icon plus the text carry the meaning, not color.
  const kind = $derived(
    connection.resetting
      ? 'resetting'
      : !connection.online
        ? 'offline'
        : session.authenticated && connection.reconnecting
          ? 'reconnecting'
          : null,
  );
</script>

<div class="slot" role="status" aria-live="polite">
  {#if kind}
    <div class="banner {kind}">
      {#if kind === 'resetting'}
        <ServerCog aria-hidden="true" size={18} />
        <span>{t.banner.resetting}</span>
        <button class="btn" type="button" onclick={() => void retryServer()}>
          <RefreshCw aria-hidden="true" size={16} />
          {t.banner.retry}
        </button>
      {:else if kind === 'offline'}
        <WifiOff aria-hidden="true" size={18} />
        <span>{t.banner.offline}</span>
      {:else}
        <RefreshCw aria-hidden="true" size={18} />
        <span>{t.banner.reconnecting}</span>
      {/if}
    </div>
  {/if}
</div>

<style>
  .banner {
    display: flex;
    align-items: center;
    gap: var(--s2);
    padding: var(--s2) var(--s4);
    border-bottom: 1px solid var(--line-strong);
  }
  .banner.resetting {
    background: var(--badbg);
    color: var(--bad);
  }
  .banner.offline,
  .banner.reconnecting {
    background: var(--warnbg);
    color: var(--warn);
  }
  .banner .btn {
    min-height: 32px;
    padding: var(--s1) var(--s3);
    color: var(--fg);
    margin-left: auto;
  }
</style>
