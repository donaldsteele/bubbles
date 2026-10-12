<script lang="ts">
  import { Bell, Search, Settings, Shield } from '@lucide/svelte';
  import { t } from '../../lib/i18n/en';
  import { currentRoute } from '../../lib/router';
  import { session } from '../../lib/state/session.svelte';
  import { settings } from '../../lib/state/settings.svelte';
  import { ui } from '../../lib/state/ui.svelte';
  import ChannelList from '../channels/ChannelList.svelte';

  let { route }: { route: ReturnType<typeof currentRoute> } = $props();
</script>

<div class="side">
  <p class="instance">{settings.instanceName || t.app.name}</p>
  <div class="quick">
    <button class="btn" type="button" onclick={() => (ui.searchOpen = true)}>
      <Search aria-hidden="true" size={16} />
      {t.rich.search}
    </button>
    <a href="/alerts" aria-current={route.name === 'alerts' ? 'page' : undefined}>
      <Bell aria-hidden="true" size={16} />
      {t.rich.alertsNav}
    </a>
  </div>
  <ChannelList {route} />
  <nav class="foot" aria-label={t.nav.settings}>
    <a href="/settings"><Settings aria-hidden="true" size={16} /> {t.nav.settings}</a>
    {#if session.isAdmin}
      <a href="/admin"><Shield aria-hidden="true" size={16} /> {t.nav.admin}</a>
    {/if}
  </nav>
</div>

<style>
  .quick {
    display: flex;
    flex-direction: column;
    gap: var(--s1);
  }
  .quick .btn {
    display: inline-flex;
    align-items: center;
    gap: var(--s2);
    justify-content: flex-start;
    min-height: 36px;
  }
  .side {
    display: flex;
    flex-direction: column;
    gap: var(--s3);
    padding: var(--s3);
    height: 100%;
  }
  .instance {
    font-weight: 700;
    margin: 0;
  }
  a {
    display: flex;
    align-items: center;
    gap: var(--s2);
    min-height: 44px;
    padding: 0 var(--s2);
    color: var(--fg);
    text-decoration: none;
    border-radius: var(--radius);
  }
  a:hover {
    background: var(--bg-elev2);
  }
  .foot {
    margin-top: auto;
  }
</style>
