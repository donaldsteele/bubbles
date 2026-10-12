<script lang="ts">
  import { onMount } from 'svelte';
  import ResetComplete from './components/admin/ResetComplete.svelte';
  import AlertsView from './components/alerts/AlertsView.svelte';
  import MembersPanel from './components/channels/MembersPanel.svelte';
  import PinsPanel from './components/pins/PinsPanel.svelte';
  import SearchOverlay from './components/search/SearchOverlay.svelte';
  import SettingsPage from './components/settings/SettingsPage.svelte';
  import QuickSwitcher from './components/switcher/QuickSwitcher.svelte';
  import AppShell from './components/layout/AppShell.svelte';
  import ThreadPanel from './components/messages/ThreadPanel.svelte';
  import ServerResetNotice from './components/common/ServerResetNotice.svelte';
  import UpdatePrompt from './components/common/UpdatePrompt.svelte';
  import ConnectionBanner from './components/layout/ConnectionBanner.svelte';
  import { resetFlow } from './lib/admin/resetFlow.svelte';
  import { boot, wireApp } from './lib/boot';
  import { takeAround } from './lib/jump';
  import { t } from './lib/i18n/en';
  import {
    currentRoute,
    focusMainHeading,
    installRouter,
    revalidate,
    subscribe,
    type Route,
  } from './lib/router';
  import { connection } from './lib/state/connection.svelte';
  import { resetNotice } from './lib/state/resetNotice.svelte';
  import { session } from './lib/state/session.svelte';
  import { ui } from './lib/state/ui.svelte';
  import AdminLayout from './routes/admin/AdminLayout.svelte';
  import InviteAccept from './routes/InviteAccept.svelte';
  import Channel from './routes/Channel.svelte';
  import Login from './routes/Login.svelte';
  import NotFound from './routes/NotFound.svelte';
  import PasswordReset from './routes/PasswordReset.svelte';
  import Placeholder from './routes/Placeholder.svelte';
  import Register from './routes/Register.svelte';

  let route = $state<Route>(currentRoute());

  onMount(() => {
    resetNotice.restore();
    wireApp();
    connection.online = navigator.onLine;
    const on = () => (connection.online = true);
    const off = () => (connection.online = false);
    window.addEventListener('online', on);
    window.addEventListener('offline', off);
    const unsub = subscribe((r) => (route = r));
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === 'k') {
        if (!session.authenticated) return;
        e.preventDefault();
        ui.switcherOpen = !ui.switcherOpen;
      }
    };
    window.addEventListener('keydown', onKey);
    let uninstall = () => {};
    void boot().then(() => {
      uninstall = installRouter();
      focusMainHeading();
    });
    return () => {
      unsub();
      uninstall();
      window.removeEventListener('keydown', onKey);
      window.removeEventListener('online', on);
      window.removeEventListener('offline', off);
    };
  });

  // Sign-in and sign-out change what the current URL may show.
  $effect(() => {
    void session.authenticated;
    if (session.ready) revalidate();
  });

  // A notification click or search result: /c/<id>?around=<message_id>.
  $effect(() => {
    if (session.authenticated) takeAround(route);
  });

  // Pins and members belong to one channel.
  $effect(() => {
    void route.params['channelId'];
    ui.panel = null;
  });

  const panelOpen = $derived(route.name === 'channel' && ui.panel !== null);

  function closePanel() {
    ui.panel = null;
    const trigger = ui.drawerTrigger;
    ui.drawerTrigger = null;
    if (trigger?.isConnected) trigger.focus();
  }

  const publicPage = $derived(
    ['login', 'register', 'invite', 'reset', 'notfound'].includes(route.name) ||
      !session.authenticated,
  );
</script>

<UpdatePrompt />
<ServerResetNotice />
{#if resetFlow.active}
  <ResetComplete />
{/if}

{#if !session.ready}
  <p class="visually-hidden" role="status">{t.app.loading}</p>
{:else if publicPage}
  <ConnectionBanner />
  {#if route.name === 'login'}
    <Login />
  {:else if route.name === 'register'}
    <Register />
  {:else if route.name === 'invite'}
    <InviteAccept code={route.params.code ?? ''} />
  {:else if route.name === 'reset'}
    <PasswordReset code={route.params.code ?? ''} />
  {:else}
    <NotFound />
  {/if}
{:else}
  {#snippet threadDrawer()}
    <ThreadPanel channelId={route.params.channelId ?? ''} rootId={route.params.messageId ?? ''} />
  {/snippet}
  {#snippet panelDrawer()}
    {#if ui.panel === 'pins'}
      <PinsPanel channelId={route.params.channelId ?? ''} onclose={closePanel} />
    {:else if ui.panel === 'members'}
      <MembersPanel channelId={route.params.channelId ?? ''} onclose={closePanel} />
    {/if}
  {/snippet}
  <AppShell
    {route}
    flush={route.name === 'channel' || route.name === 'thread'}
    drawer={route.name === 'thread' ? threadDrawer : panelOpen ? panelDrawer : undefined}
    drawerLabel={route.name === 'thread'
      ? t.chat.threadTitle
      : ui.panel === 'pins'
        ? t.rich.pinsTitle
        : t.rich.membersTitle}
  >
    {#if route.name === 'home'}
      <Placeholder title={t.home.title} body={t.home.body} />
    {:else if route.name === 'channel' || route.name === 'thread'}
      <Channel channelId={route.params.channelId ?? ''} />
    {:else if route.name === 'settings'}
      <SettingsPage {route} />
    {:else if route.name === 'alerts'}
      <AlertsView />
    {:else if route.name === 'admin'}
      <AdminLayout {route} />
    {/if}
  </AppShell>
  {#if ui.searchOpen}
    <SearchOverlay onclose={() => (ui.searchOpen = false)} />
  {/if}
  {#if ui.switcherOpen}
    <QuickSwitcher onclose={() => (ui.switcherOpen = false)} />
  {/if}
{/if}
