<script lang="ts">
  import { TriangleAlert, Trash2 } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import ResetDialog from '../../components/admin/ResetDialog.svelte';
  import { api, call, type Schemas } from '../../lib/api/client';
  import { formatBytes } from '../../lib/admin/format';
  import { failureFrom } from '../../lib/forms';
  import { fmt, t } from '../../lib/i18n/en';
  import { settings } from '../../lib/state/settings.svelte';

  let preview = $state<Schemas['ResetPreview'] | null>(null);
  let error = $state('');
  let dialog = $state<'content' | 'factory' | null>(null);
  let cleared = $state(false);

  const instanceName = $derived(settings.instanceName);

  async function load() {
    try {
      preview = await call(api.GET('/admin/reset/preview'));
      error = '';
    } catch (err) {
      error = failureFrom(err).form;
    }
  }
  onMount(() => void load());

  function n(v: number | undefined): string {
    return v === undefined ? '…' : String(v);
  }

  function oncleared() {
    cleared = true;
    void load();
  }

  function open(mode: 'content' | 'factory') {
    cleared = false;
    dialog = mode;
  }
</script>

<section aria-labelledby="dz-title">
  <h2 id="dz-title">{t.admin.tabDanger}</h2>
  <p class="lead">{t.danger.lead}</p>
  {#if error}<p class="form-error" role="alert">{error}</p>{/if}
  {#if cleared}<p class="done" role="status">{t.danger.clearedDone}</p>{/if}

  <div class="cards">
    <section class="card danger-card" aria-labelledby="dz-content">
      <h3 id="dz-content">
        <TriangleAlert aria-hidden="true" size={18} />
        {t.danger.contentTitle}
      </h3>
      <p>{t.danger.contentIntro}</p>
      <h4>{t.danger.deleted}</h4>
      <ul>
        <li>{fmt(t.danger.cMessages, { n: n(preview?.messages) })}</li>
        <li>
          {fmt(t.danger.cFiles, {
            n: n(preview?.files),
            size: preview ? formatBytes(preview.bytes) : '…',
          })}
        </li>
        <li>{t.danger.cOther}</li>
        <li>{t.danger.cIndex}</li>
        <li>{t.danger.cUnread}</li>
      </ul>
      <h4>{t.danger.kept}</h4>
      <ul>
        <li>{fmt(t.danger.kUsers, { n: n(preview?.users) })}</li>
        <li>{fmt(t.danger.kChannels, { n: n(preview?.channels) })}</li>
        <li>{t.danger.kOther}</li>
      </ul>
      <button class="btn danger" type="button" onclick={() => open('content')}>
        <Trash2 aria-hidden="true" size={16} />
        {t.danger.deleteContentOpen}
      </button>
    </section>

    <section class="card danger-card" aria-labelledby="dz-factory">
      <h3 id="dz-factory">
        <TriangleAlert aria-hidden="true" size={18} />
        {t.danger.factoryTitle}
      </h3>
      <p>{t.danger.factoryIntro}</p>
      <h4>{t.danger.deleted}</h4>
      <ul>
        <li>{fmt(t.danger.fUsers, { n: n(preview?.users) })}</li>
        <li>{fmt(t.danger.fChannels, { n: n(preview?.channels) })}</li>
        <li>{fmt(t.danger.cMessages, { n: n(preview?.messages) })}</li>
        <li>
          {fmt(t.danger.cFiles, {
            n: n(preview?.files),
            size: preview ? formatBytes(preview.bytes) : '…',
          })}
        </li>
        <li>{t.danger.fOther}</li>
        <li>{t.danger.fLogs}</li>
      </ul>
      <h4>{t.danger.kept}</h4>
      <ul>
        <li>{t.danger.fKeptCa}</li>
        <li>{t.danger.fKeptSettings}</li>
        <li>{t.danger.fKeptBackups}</li>
      </ul>
      <button class="btn danger" type="button" onclick={() => open('factory')}>
        <Trash2 aria-hidden="true" size={16} />
        {t.danger.deleteEverythingOpen}
      </button>
    </section>
  </div>
</section>

{#if dialog}
  <ResetDialog mode={dialog} {instanceName} onclose={() => (dialog = null)} {oncleared} />
{/if}

<style>
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
    gap: var(--s5);
  }
  .card {
    width: auto;
    margin: 0;
    border: 2px solid var(--bad);
    display: flex;
    flex-direction: column;
    gap: var(--s1);
  }
  h3 {
    display: flex;
    align-items: center;
    gap: var(--s2);
    margin: 0;
    font-size: 17px;
    color: var(--bad);
  }
  h4 {
    margin: var(--s2) 0 0;
    font-size: 14px;
  }
  ul {
    margin: 0;
    padding-left: var(--s4);
  }
  p {
    margin: 0;
  }
  .btn {
    margin-top: auto;
    align-self: flex-start;
    display: inline-flex;
    align-items: center;
    gap: var(--s2);
  }
  .done {
    border: 1px solid var(--ok);
    background: var(--okbg);
    padding: var(--s3);
    border-radius: var(--radius);
  }
</style>
