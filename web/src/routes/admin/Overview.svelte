<script lang="ts">
  import { onMount } from 'svelte';
  import QrImage from '../../components/common/QrImage.svelte';
  import { api, call, type Schemas } from '../../lib/api/client';
  import {
    daysUntil,
    formatBytes,
    formatDateTime,
    formatUptime,
    groupFingerprint,
  } from '../../lib/admin/format';
  import { failureFrom } from '../../lib/forms';
  import { fmt, t } from '../../lib/i18n/en';

  let data = $state<Schemas['AdminOverview'] | null>(null);
  let error = $state('');

  onMount(() => {
    void call(api.GET('/admin/overview')).then(
      (o) => (data = o),
      (err: unknown) => (error = failureFrom(err).form),
    );
  });

  const certDays = $derived(data?.cert_not_after ? daysUntil(data.cert_not_after) : null);
</script>

<section class="ov" aria-labelledby="ov-title">
  <h2 id="ov-title">{t.admin.tabOverview}</h2>
  {#if error}<p class="form-error" role="alert">{error}</p>{/if}
  {#if !data && !error}<p role="status">{t.app.loading}</p>{/if}
  {#if data}
    {#if data.client_isolation_hint}
      <div class="callout" role="note">
        <strong>{t.admin.isolationTitle}</strong>
        <p>{t.admin.isolationBody}</p>
      </div>
    {/if}

    <dl class="stats">
      <div>
        <dt>{t.admin.version}</dt>
        <dd>{data.version}</dd>
      </div>
      <div>
        <dt>{t.admin.uptime}</dt>
        <dd>{formatUptime(data.uptime_seconds)}</dd>
      </div>
      <div>
        <dt>{t.admin.usersCount}</dt>
        <dd>{data.users}</dd>
      </div>
      <div>
        <dt>{t.admin.connections}</dt>
        <dd>{data.connections}</dd>
      </div>
      <div>
        <dt>{t.admin.storageUsed}</dt>
        <dd>{formatBytes(data.storage_used_bytes)}</dd>
      </div>
      <div>
        <dt>{t.admin.diskFree}</dt>
        <dd>
          {data.disk_free_bytes === null ? t.admin.unknown : formatBytes(data.disk_free_bytes)}
        </dd>
      </div>
    </dl>

    <h2>{t.admin.addresses}</h2>
    <p class="lead">{t.admin.addressesHelp}</p>
    <div class="grid">
      {#each data.https_urls as url (url)}
        <div class="panel">
          <h3>{t.admin.httpsUrl}</h3>
          <p class="mono"><a href={url}>{url}</a></p>
          <QrImage value={url} label={fmt(t.admin.qrFor, { url })} />
        </div>
      {/each}
      {#each data.onboarding_urls as url (url)}
        <div class="panel">
          <h3>{t.admin.onboardingUrl}</h3>
          <p class="mono"><a href={url}>{url}</a></p>
          <p class="hint">{t.admin.onboardingHelp}</p>
          <QrImage value={url} label={fmt(t.admin.qrFor, { url })} />
        </div>
      {/each}
    </div>

    <h2>{t.admin.certificate}</h2>
    <dl class="stats">
      <div>
        <dt>{t.admin.caFingerprint}</dt>
        <dd class="mono">
          {data.ca_fingerprint ? groupFingerprint(data.ca_fingerprint) : t.admin.none}
        </dd>
      </div>
      <div>
        <dt>{t.admin.certExpiry}</dt>
        <dd>
          {formatDateTime(data.cert_not_after)}
          {#if certDays !== null}
            <span class="chip" class:bad={certDays < 30}
              >{certDays < 0
                ? t.admin.expired
                : fmt(t.admin.daysLeft, { n: String(certDays) })}</span
            >
          {/if}
        </dd>
      </div>
    </dl>
  {/if}
</section>

<style>
  .stats {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: var(--s3);
    margin: 0 0 var(--s3);
  }
  dt {
    color: var(--fg-muted);
    font-size: 13px;
  }
  dd {
    margin: 0;
    font-size: 18px;
    font-weight: 600;
  }
  dd.mono {
    font-size: 14px;
  }
  h3 {
    margin: 0 0 var(--s1);
    font-size: 15px;
  }
  .panel p {
    margin: var(--s1) 0;
  }
</style>
