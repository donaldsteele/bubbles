<script lang="ts">
  import type { Snippet } from 'svelte';
  import { Menu } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import { outbox } from '../../lib/composer/outbox.svelte';
  import { groupChannels, orderedIds } from '../../lib/channels';
  import { t } from '../../lib/i18n/en';
  import { installKeyboard } from '../../lib/keyboard';
  import type { Route } from '../../lib/router';
  import { channels } from '../../lib/state/channels.svelte';
  import { connection } from '../../lib/state/connection.svelte';
  import { session } from '../../lib/state/session.svelte';
  import { unread } from '../../lib/state/unread.svelte';
  import { users } from '../../lib/state/users.svelte';
  import ConnectionBanner from './ConnectionBanner.svelte';
  import Sidebar from './Sidebar.svelte';

  interface Props {
    route: Route;
    children: Snippet;
    /** Optional right drawer (thread, pins, members). */
    drawer?: Snippet;
    /** Edge-to-edge main area for panes that manage their own scrolling. */
    flush?: boolean;
    /** Accessible name of the drawer. */
    drawerLabel?: string;
  }

  let { route, children, drawer, flush = false, drawerLabel }: Props = $props();

  // >= 1024px: sidebar is always visible. 640-1023px: it is a rail that this
  // toggle expands. < 640px: it is hidden until the toggle opens it.
  let navOpen = $state(false);

  const ids = $derived(
    orderedIds(groupChannels(channels.joined, users.byId, session.me?.id ?? '')),
  );

  onMount(() =>
    installKeyboard({
      channelIds: () => ids,
      currentChannel: () => route.params['channelId'],
      hasUnread: (id) => unread.get(id).unread > 0,
    }),
  );

  // Pending sends go out again, with their original nonces, once the socket is back.
  $effect(() => {
    if (connection.ws === 'open') void outbox.flush();
  });

  // Navigating closes the overlay.
  $effect(() => {
    void route.path;
    navOpen = false;
  });
</script>

<a class="skip-link" href="#main">{t.app.skipToMain}</a>
<ConnectionBanner />
<div class="shell" class:nav-open={navOpen} class:has-drawer={!!drawer}>
  <header class="topbar">
    <button
      class="btn toggle"
      type="button"
      aria-expanded={navOpen}
      aria-controls="sidebar"
      aria-label={t.nav.toggle}
      onclick={() => (navOpen = !navOpen)}
    >
      <Menu aria-hidden="true" size={20} />
    </button>
  </header>
  <aside id="sidebar" class="sidebar" aria-label={t.nav.sidebar}>
    <Sidebar {route} />
  </aside>
  <div id="main" class="main-slot" class:flush>
    <main class:flush>{@render children()}</main>
  </div>
  {#if drawer}
    <aside class="drawer" data-region="drawer" aria-label={drawerLabel ?? t.chat.threadTitle}>
      {@render drawer()}
    </aside>
  {/if}
</div>

<style>
  .shell {
    display: grid;
    height: 100%;
    min-height: 0;
    grid-template-columns: 240px minmax(0, 1fr);
    grid-template-areas:
      'side main'
      'side main';
  }
  .shell.has-drawer {
    grid-template-columns: 240px minmax(0, 1fr) 320px;
    grid-template-areas: 'side main drawer';
  }
  .topbar {
    display: none;
  }
  .sidebar {
    grid-area: side;
    background: var(--bg-elev);
    border-right: 1px solid var(--line);
    overflow-y: auto;
  }
  .main-slot {
    grid-area: main;
    overflow-y: auto;
    min-width: 0;
  }
  main {
    padding: var(--s4);
  }
  .main-slot.flush {
    overflow: hidden;
  }
  main.flush {
    padding: 0;
    height: 100%;
  }
  .drawer {
    grid-area: drawer;
    background: var(--bg-elev);
    border-left: 1px solid var(--line);
    overflow-y: auto;
  }

  /* Tablet: collapsible rail that expands over the content. */
  @media (max-width: 1023px) {
    .shell,
    .shell.has-drawer {
      grid-template-columns: 56px minmax(0, 1fr);
      grid-template-areas: 'side main';
    }
    .topbar {
      display: block;
      position: fixed;
      top: var(--s2);
      left: var(--s2);
      z-index: 20;
    }
    .sidebar {
      width: 56px;
      overflow: hidden;
      padding-top: 56px;
    }
    .nav-open .sidebar {
      position: fixed;
      inset: 0 auto 0 0;
      width: 280px;
      z-index: 10;
      overflow-y: auto;
      box-shadow: 0 0 24px var(--shadow);
    }
    .drawer {
      position: fixed;
      inset: 0 0 0 auto;
      width: min(320px, 100%);
      z-index: 10;
    }
  }

  /* Phone: stacked, the list is a full-width overlay. */
  @media (max-width: 639px) {
    .shell,
    .shell.has-drawer {
      grid-template-columns: minmax(0, 1fr);
      grid-template-areas: 'main';
      padding-bottom: env(safe-area-inset-bottom);
    }
    .sidebar {
      display: none;
    }
    .nav-open .sidebar {
      display: block;
      width: 100%;
      padding-top: 56px;
    }
    main {
      padding-top: 56px;
    }
    main.flush {
      padding-top: 56px;
    }
    /* A thread, pins or members replace the channel view; Close steps back. */
    .drawer {
      width: 100%;
      padding-top: 56px;
    }
  }
</style>
