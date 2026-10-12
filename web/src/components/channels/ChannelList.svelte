<script lang="ts">
  import { AtSign, Hash, Lock, Plus } from '@lucide/svelte';
  import { channelLabel, groupChannels } from '../../lib/channels';
  import { t } from '../../lib/i18n/en';
  import type { Route } from '../../lib/router';
  import { channels } from '../../lib/state/channels.svelte';
  import { session } from '../../lib/state/session.svelte';
  import { unread } from '../../lib/state/unread.svelte';
  import { users } from '../../lib/state/users.svelte';
  import PresenceDot from '../users/PresenceDot.svelte';
  import BrowseChannels from './BrowseChannels.svelte';
  import CreateChannel from './CreateChannel.svelte';
  import NewDM from './NewDM.svelte';

  let { route }: { route: Route } = $props();

  const meId = $derived(session.me?.id ?? '');
  const groups = $derived(groupChannels(channels.joined, users.byId, meId));
  const canCreate = $derived(session.me?.role !== 'guest');
  let dialog = $state<'browse' | 'create' | 'dm' | null>(null);

  function otherId(participants: string[]): string {
    return participants.find((p) => p !== meId) ?? meId;
  }
</script>

{#snippet badges(count: number, mentions: number)}
  {#if mentions > 0}
    <span class="badge mention">
      <AtSign aria-hidden="true" size={12} />{mentions}
      <span class="visually-hidden"> {t.chat.mentions}</span>
    </span>
  {:else if count > 0}
    <span class="badge">{count}<span class="visually-hidden"> {t.chat.unread}</span></span>
  {/if}
{/snippet}

<nav aria-label={t.nav.channels} data-region="sidebar">
  <section aria-labelledby="sec-channels">
    <div class="sec-head">
      <h2 id="sec-channels">{t.chat.channels}</h2>
      {#if canCreate}
        <button
          class="icon"
          type="button"
          aria-label={t.chat.create}
          title={t.chat.create}
          onclick={() => (dialog = 'create')}
        >
          <Plus aria-hidden="true" size={16} />
        </button>
      {/if}
    </div>
    <ul>
      {#each groups.channels as ch (ch.id)}
        {@const n = unread.get(ch.id)}
        <li>
          <a
            href={`/c/${ch.id}`}
            class:unread={n.unread > 0}
            aria-current={route.params.channelId === ch.id ? 'page' : undefined}
          >
            {#if ch.kind === 'private'}
              <Lock aria-hidden="true" size={16} />
            {:else}
              <Hash aria-hidden="true" size={16} />
            {/if}
            <span class="name">{ch.name ?? ch.id}</span>
            {@render badges(n.unread, n.mentions)}
          </a>
        </li>
      {/each}
    </ul>
    {#if canCreate}
      <button class="link" type="button" onclick={() => (dialog = 'browse')}>{t.chat.browse}</button
      >
    {/if}
  </section>

  <section aria-labelledby="sec-dms">
    <div class="sec-head">
      <h2 id="sec-dms">{t.chat.directMessages}</h2>
      <button
        class="icon"
        type="button"
        aria-label={t.chat.newDm}
        title={t.chat.newDm}
        onclick={() => (dialog = 'dm')}
      >
        <Plus aria-hidden="true" size={16} />
      </button>
    </div>
    <ul>
      {#each groups.dms as ch (ch.id)}
        {@const n = unread.get(ch.id)}
        <li>
          <a
            href={`/c/${ch.id}`}
            class:unread={n.unread > 0}
            aria-current={route.params.channelId === ch.id ? 'page' : undefined}
          >
            {#if ch.kind === 'dm'}
              <PresenceDot userId={otherId(ch.participant_ids ?? [])} />
            {:else}
              <Hash aria-hidden="true" size={16} />
            {/if}
            <span class="name">{channelLabel(ch, users.byId, meId)}</span>
            {@render badges(n.unread, n.mentions)}
          </a>
        </li>
      {/each}
    </ul>
  </section>
</nav>

{#if dialog === 'browse'}
  <BrowseChannels onclose={() => (dialog = null)} />
{:else if dialog === 'create'}
  <CreateChannel onclose={() => (dialog = null)} />
{:else if dialog === 'dm'}
  <NewDM onclose={() => (dialog = null)} />
{/if}

<style>
  nav {
    display: flex;
    flex-direction: column;
    gap: var(--s3);
  }
  .sec-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  h2 {
    margin: 0;
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--fg-muted);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  a {
    display: flex;
    align-items: center;
    gap: var(--s2);
    min-height: 36px;
    padding: 0 var(--s2);
    color: var(--fg);
    text-decoration: none;
    border-radius: var(--radius);
  }
  a:hover,
  a[aria-current='page'] {
    background: var(--bg-elev2);
  }
  a[aria-current='page'] {
    box-shadow: inset 3px 0 0 var(--accent);
  }
  a.unread .name {
    font-weight: 700;
  }
  .name {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .badge {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    border: 1px solid var(--line-strong);
    border-radius: 999px;
    padding: 0 var(--s2);
    font-size: 12px;
    font-weight: 700;
  }
  .badge.mention {
    background: var(--accent);
    color: var(--accent-fg);
    border-color: var(--accent);
  }
  .icon {
    min-width: 28px;
    min-height: 28px;
    display: grid;
    place-items: center;
    background: none;
    border: 1px solid transparent;
    border-radius: var(--radius);
    cursor: pointer;
  }
  .icon:hover {
    border-color: var(--line-strong);
  }
  .link {
    background: none;
    border: 0;
    padding: var(--s1) var(--s2);
    color: var(--link);
    cursor: pointer;
    text-decoration: underline;
    min-height: 28px;
  }
</style>
