<script lang="ts">
  import { onMount } from 'svelte';
  import { api, call, type Schemas } from '../../lib/api/client';
  import { daysUntil, formatDateTime, groupFingerprint } from '../../lib/admin/format';
  import { failureFrom } from '../../lib/forms';
  import { fmt, t } from '../../lib/i18n/en';

  let tls = $state<Schemas['AdminTLS'] | null>(null);
  let onboarding = $state('');
  let error = $state('');
  let busy = $state(false);
  let done = $state('');

  onMount(() => {
    void call(api.GET('/admin/tls')).then(
      (s) => (tls = s),
      (err: unknown) => (error = failureFrom(err).form),
    );
    void call(api.GET('/admin/overview')).then(
      (o) => (onboarding = (o.onboarding_urls[0] ?? '').replace(/\/$/, '')),
      () => {},
    );
  });

  async function reissue() {
    busy = true;
    error = '';
    done = '';
    try {
      tls = await call(api.POST('/admin/tls/reissue'));
      done = t.admin.reissued;
    } catch (err) {
      error = failureFrom(err).form;
    } finally {
      busy = false;
    }
  }

  const byo = $derived(tls?.mode === 'byo');
</script>

<section aria-labelledby="sec-title">
  <h2 id="sec-title">{t.admin.tabSecurity}</h2>
  {#if error}<p class="form-error" role="alert">{error}</p>{/if}
  {#if !tls && !error}<p role="status">{t.app.loading}</p>{/if}
  {#if tls}
    <dl class="info">
      <div>
        <dt>{t.admin.tlsMode}</dt>
        <dd>{byo ? t.admin.tlsByo : t.admin.tlsLocalCa}</dd>
      </div>
      {#if !byo}
        <div>
          <dt>{t.admin.caFingerprint}</dt>
          <dd class="mono">{groupFingerprint(tls.ca_fingerprint)}</dd>
        </div>
      {/if}
      <div>
        <dt>{t.admin.leafFingerprint}</dt>
        <dd class="mono">{groupFingerprint(tls.leaf_fingerprint)}</dd>
      </div>
      <div>
        <dt>{t.admin.certExpiry}</dt>
        <dd>
          {formatDateTime(tls.leaf_not_after)}
          <span class="chip" class:bad={daysUntil(tls.leaf_not_after) < 30}
            >{fmt(t.admin.daysLeft, { n: String(daysUntil(tls.leaf_not_after)) })}</span
          >
        </dd>
      </div>
    </dl>

    <h3>{t.admin.sans}</h3>
    <ul>
      {#each tls.sans as san (san)}<li class="mono">{san}</li>{/each}
    </ul>

    <h3>{t.admin.reissueTitle}</h3>
    <p class="lead">{byo ? t.admin.reissueByo : t.admin.reissueHelp}</p>
    <button class="btn" type="button" disabled={busy || byo} onclick={() => void reissue()}>
      {busy ? t.common.submitting : t.admin.reissue}
    </button>
    {#if done}<p role="status">{done}</p>{/if}

    {#if !byo}
      <h3>{t.admin.caDownload}</h3>
      <p class="lead">{t.admin.caDownloadHelp}</p>
      {#if onboarding}
        <ul>
          <li><a href={`${onboarding}/ca.crt`}>{t.admin.caCrt}</a></li>
          <li><a href={`${onboarding}/ca.pem`}>{t.admin.caPem}</a></li>
          <li><a href={`${onboarding}/ca.mobileconfig`}>{t.admin.caMobileconfig}</a></li>
          <li><a href={`${onboarding}/`}>{t.admin.caPage}</a></li>
        </ul>
      {/if}
    {/if}
  {/if}
</section>

<style>
  .info {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: var(--s3);
    margin: 0;
  }
  dt {
    color: var(--fg-muted);
    font-size: 13px;
  }
  dd {
    margin: 0;
  }
  h3 {
    margin: var(--s4) 0 var(--s2);
    font-size: 16px;
  }
</style>
