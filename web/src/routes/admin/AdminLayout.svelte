<script lang="ts">
  import '../../styles/admin.css';
  import { t } from '../../lib/i18n/en';
  import type { Route } from '../../lib/router';
  import Apps from './Apps.svelte';
  import Audit from './Audit.svelte';
  import Channels from './Channels.svelte';
  import DangerZone from './DangerZone.svelte';
  import Invites from './Invites.svelte';
  import Overview from './Overview.svelte';
  import Pairing from './Pairing.svelte';
  import Security from './Security.svelte';
  import Settings from './Settings.svelte';
  import Users from './Users.svelte';
  import Webhooks from './Webhooks.svelte';

  let { route }: { route: Route } = $props();

  const tabs = [
    { key: '', label: t.admin.tabOverview },
    { key: 'users', label: t.admin.tabUsers },
    { key: 'invites', label: t.admin.tabInvites },
    { key: 'channels', label: t.admin.tabChannels },
    { key: 'apps', label: t.admin.tabApps },
    { key: 'pairing', label: t.admin.tabPairing },
    { key: 'webhooks', label: t.admin.tabWebhooks },
    { key: 'settings', label: t.admin.tabSettings },
    { key: 'audit', label: t.admin.tabAudit },
    { key: 'security', label: t.admin.tabSecurity },
    { key: 'danger', label: t.admin.tabDanger },
  ];

  const section = $derived(tabs.some((x) => x.key === route.rest) ? route.rest : '');
  const current = $derived(tabs.find((x) => x.key === section)?.label ?? '');
</script>

<div class="adm">
  <h1 tabindex="-1">{t.adminPage.title}</h1>
  <nav class="tabs" aria-label={t.admin.nav}>
    {#each tabs as tab (tab.key)}
      <a
        href={tab.key === '' ? '/admin' : `/admin/${tab.key}`}
        class:danger={tab.key === 'danger'}
        aria-current={section === tab.key ? 'page' : undefined}>{tab.label}</a
      >
    {/each}
  </nav>
  <p class="visually-hidden" role="status">{current}</p>

  {#if section === 'users'}
    <Users />
  {:else if section === 'invites'}
    <Invites />
  {:else if section === 'channels'}
    <Channels />
  {:else if section === 'apps'}
    <Apps />
  {:else if section === 'pairing'}
    <Pairing />
  {:else if section === 'webhooks'}
    <Webhooks />
  {:else if section === 'settings'}
    <Settings />
  {:else if section === 'audit'}
    <Audit />
  {:else if section === 'security'}
    <Security />
  {:else if section === 'danger'}
    <DangerZone />
  {:else}
    <Overview />
  {/if}
</div>

<style>
  .adm {
    max-width: 960px;
  }
  h1 {
    margin-top: 0;
    font-size: 22px;
  }
  .tabs {
    display: flex;
    flex-wrap: wrap;
    gap: var(--s2);
    margin-bottom: var(--s4);
    border-bottom: 1px solid var(--line);
  }
  .tabs a {
    display: inline-flex;
    align-items: center;
    min-height: 40px;
    padding: 0 var(--s2);
  }
  .tabs a[aria-current='page'] {
    border-bottom: 2px solid var(--accent);
    font-weight: 600;
  }
  .tabs a.danger {
    margin-left: auto;
    color: var(--bad);
  }
</style>
