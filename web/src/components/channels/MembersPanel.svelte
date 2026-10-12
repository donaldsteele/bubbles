<script lang="ts">
  import { X } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import { api, call } from '../../lib/api/client';
  import type { Schemas } from '../../lib/api/client';
  import { t } from '../../lib/i18n/en';
  import { users } from '../../lib/state/users.svelte';
  import PresenceDot from '../users/PresenceDot.svelte';

  let { channelId, onclose }: { channelId: string; onclose: () => void } = $props();

  let members = $state<Schemas['Member'][]>([]);
  let loading = $state(true);
  let failed = $state(false);

  onMount(() => {
    call(api.GET('/channels/{id}/members', { params: { path: { id: channelId } } }))
      .then((r) => {
        members = r.items;
      })
      .catch(() => {
        failed = true;
      })
      .finally(() => {
        loading = false;
      });
  });

  function name(id: string): string {
    const u = users.get(id);
    return u ? u.display_name || u.username : id;
  }

  function onkeydown(ev: KeyboardEvent) {
    if (ev.key === 'Escape') {
      ev.preventDefault();
      onclose();
    }
  }
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<section class="panel" aria-labelledby="members-title" {onkeydown}>
  <header>
    <h2 id="members-title">{t.rich.membersTitle}</h2>
    <button
      class="icon"
      type="button"
      data-autofocus
      aria-label={t.rich.close}
      title={t.rich.close}
      onclick={onclose}
    >
      <X aria-hidden="true" size={18} />
    </button>
  </header>
  <div class="list">
    {#if loading}
      <p class="status" role="status">{t.app.loading}</p>
    {:else if failed}
      <p class="status" role="alert">{t.chat.loadFailed}</p>
    {/if}
    <ul role="list">
      {#each members as m (m.user_id)}
        <li>
          <PresenceDot userId={m.user_id} />
          <span class="name">{name(m.user_id)}</span>
          {#if m.role === 'owner'}<span class="chip">{t.rich.owner}</span>{/if}
        </li>
      {/each}
    </ul>
  </div>
</section>

<style>
  .panel {
    display: flex;
    flex-direction: column;
    height: 100%;
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: var(--s2) var(--s3);
    border-bottom: 1px solid var(--line);
  }
  h2 {
    margin: 0;
    font-size: 16px;
  }
  .list {
    flex: 1;
    overflow-y: auto;
    padding: var(--s2) var(--s3);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    display: flex;
    align-items: center;
    gap: var(--s2);
    min-height: 32px;
  }
  .name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .chip {
    border: 1px solid var(--line-strong);
    border-radius: 999px;
    padding: 0 var(--s2);
    font-size: 11px;
    text-transform: uppercase;
  }
  .status {
    color: var(--fg-muted);
  }
  .icon {
    min-width: 32px;
    min-height: 32px;
    display: grid;
    place-items: center;
    background: none;
    border: 0;
    cursor: pointer;
  }
</style>
