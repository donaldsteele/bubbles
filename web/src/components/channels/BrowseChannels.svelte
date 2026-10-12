<script lang="ts">
  import { onMount } from 'svelte';
  import { ApiError } from '../../lib/api/client';
  import type { Channel } from '../../lib/api/types';
  import { joinChannel, listPublicChannels } from '../../lib/channel-actions';
  import { t } from '../../lib/i18n/en';
  import { navigate } from '../../lib/router';
  import { channels } from '../../lib/state/channels.svelte';
  import Dialog from '../a11y/Dialog.svelte';

  let { onclose }: { onclose: () => void } = $props();

  let items = $state<Channel[]>([]);
  let loading = $state(true);
  let error = $state('');
  let busy = $state('');

  onMount(async () => {
    try {
      items = (await listPublicChannels()).filter((c) => c.archived_at === null);
    } catch (e) {
      error = e instanceof ApiError && e.status === 403 ? t.chat.forbidden : t.common.genericError;
    } finally {
      loading = false;
    }
  });

  function isMember(id: string): boolean {
    return id in channels.memberships;
  }

  function open(id: string) {
    onclose();
    navigate(`/c/${id}`);
  }

  async function join(ch: Channel) {
    busy = ch.id;
    error = '';
    try {
      await joinChannel(ch);
      open(ch.id);
    } catch {
      error = t.common.genericError;
    } finally {
      busy = '';
    }
  }
</script>

<Dialog title={t.chat.browse} {onclose}>
  {#if error}<p class="form-error" role="alert">{error}</p>{/if}
  {#if loading}
    <p role="status">{t.app.loading}</p>
  {:else if items.length === 0 && !error}
    <p>{t.chat.noPublic}</p>
  {:else}
    <ul class="list" aria-label={t.chat.publicChannels}>
      {#each items as ch (ch.id)}
        <li>
          <div class="info">
            <strong>#{ch.name}</strong>
            <span class="muted">{ch.topic || t.chat.noTopic}</span>
            <span class="muted"
              >{ch.member_count === 1
                ? t.chat.memberCountOne
                : t.chat.memberCount.replace('{n}', String(ch.member_count))}</span
            >
          </div>
          {#if isMember(ch.id)}
            <button class="btn" type="button" onclick={() => open(ch.id)}>{t.chat.open}</button>
          {:else}
            <button
              class="btn primary"
              type="button"
              disabled={busy === ch.id}
              onclick={() => join(ch)}>{t.chat.join}</button
            >
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</Dialog>

<style>
  .list {
    list-style: none;
    margin: var(--s3) 0 0;
    padding: 0;
  }
  li {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--s3);
    padding: var(--s2) 0;
    border-bottom: 1px solid var(--line);
  }
  .info {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .muted {
    color: var(--fg-muted);
    font-size: 13px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
</style>
