<script lang="ts">
  import { meetsSeverity, listAlerts, type AlertSeverity } from '../../lib/alerts/actions';
  import type { Message } from '../../lib/api/types';
  import { t } from '../../lib/i18n/en';
  import { channels } from '../../lib/state/channels.svelte';
  import { messages } from '../../lib/state/messages.svelte';
  import AlertCard from './AlertCard.svelte';

  let tab = $state<'open' | 'acked'>('open');
  let min = $state<AlertSeverity | 'all'>('all');
  let items = $state<Message[]>([]);
  let next = $state<string | null>(null);
  let loading = $state(true);
  let failed = $state(false);

  async function load(more: boolean) {
    loading = true;
    failed = false;
    try {
      const r = await listAlerts(tab, more && next ? next : undefined);
      items = more ? [...items, ...r.items] : r.items;
      next = r.next;
    } catch {
      failed = true;
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void tab;
    void load(false);
  });

  // Ack/resolve elsewhere updates the shared store; show that state.
  const view = $derived(
    items
      .map((m) => messages.find(m.channel_id, m.id) ?? m)
      .filter((m) => m.alert?.state === tab && meetsSeverity(m.alert.severity, min)),
  );

  function chName(id: string): string {
    const c = channels.byId[id];
    return c?.name ? `#${c.name}` : '';
  }
</script>

<section class="alerts" aria-labelledby="alerts-title">
  <h1 id="alerts-title" tabindex="-1">{t.rich.alertsTitle}</h1>
  <div class="bar">
    <div class="tabs" role="group" aria-label={t.rich.alertsTitle}>
      <button class="btn" type="button" aria-pressed={tab === 'open'} onclick={() => (tab = 'open')}
        >{t.rich.alertsOpenTab}</button
      >
      <button
        class="btn"
        type="button"
        aria-pressed={tab === 'acked'}
        onclick={() => (tab = 'acked')}>{t.rich.alertsAckedTab}</button
      >
    </div>
    <label>
      <span>{t.rich.alertsFilter}</span>
      <select bind:value={min}>
        <option value="all">{t.rich.sevAll}</option>
        <option value="info">{t.rich.alertSeverity.info}+</option>
        <option value="warning">{t.rich.alertSeverity.warning}+</option>
        <option value="critical">{t.rich.alertSeverity.critical}</option>
      </select>
    </label>
  </div>

  {#if loading && items.length === 0}
    <p role="status">{t.app.loading}</p>
  {:else if failed}
    <p class="form-error" role="alert">{t.chat.loadFailed}</p>
  {:else if view.length === 0}
    <p>{t.rich.alertsEmpty}</p>
  {/if}

  <ul role="list">
    {#each view as m (m.id)}
      <li>
        <a class="where" href={`/c/${m.channel_id}`}>{chName(m.channel_id)}</a>
        <AlertCard message={m} />
      </li>
    {/each}
  </ul>
  {#if next && !loading}
    <button class="btn" type="button" onclick={() => load(true)}>{t.rich.loadMore}</button>
  {/if}
</section>

<style>
  .alerts {
    max-width: 760px;
  }
  h1 {
    margin-top: 0;
    font-size: 20px;
  }
  .bar {
    display: flex;
    flex-wrap: wrap;
    gap: var(--s3);
    align-items: end;
    margin-bottom: var(--s3);
  }
  .tabs {
    display: flex;
    gap: var(--s1);
  }
  .btn[aria-pressed='true'] {
    border-color: var(--accent);
    background: var(--bg-elev2);
  }
  label {
    display: flex;
    flex-direction: column;
    font-size: 12px;
    color: var(--fg-muted);
  }
  select {
    min-height: 36px;
    background: var(--bg-elev2);
    border: 1px solid var(--line-strong);
    border-radius: var(--radius);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    margin-bottom: var(--s3);
  }
  .where {
    font-size: 13px;
  }
</style>
