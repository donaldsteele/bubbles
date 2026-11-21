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
  const ts = $derived(tls?.tailscale);
  // The address to suggest when the local CA cannot sign the MagicDNS name.
  const tsIpv4 = $derived(ts?.ips.find((ip) => !ip.includes(':')) ?? '');

  let copied = $state(false);
  async function copyUrl(url: string) {
    try {
      await navigator.clipboard.writeText(url);
      copied = true;
    } catch {
      copied = false;
    }
  }
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

    {#if ts}
      <section aria-labelledby="ts-title">
        <h3 id="ts-title">{t.admin.tsTitle}</h3>
        <p class="lead">
          {#if !ts.running}
            {t.admin.tsNotRunning}
          {:else if ts.dns_name}
            {fmt(t.admin.tsOnTailnet, { name: ts.dns_name })}
          {:else}
            {t.admin.tsNoMagicDns}
          {/if}
        </p>
        {#if ts.ips.length > 0 || ts.url}
          <dl class="info">
            {#if ts.ips.length > 0}
              <div>
                <dt>{t.admin.tsIps}</dt>
                <dd>
                  <ul class="plain">
                    {#each ts.ips as ip (ip)}<li class="mono">{ip}</li>{/each}
                  </ul>
                </dd>
              </div>
            {/if}
            {#if ts.url}
              <div>
                <dt>{t.admin.tsUrl}</dt>
                <dd>
                  <span class="mono"><a href={ts.url}>{ts.url}</a></span>
                  <button
                    class="btn small"
                    type="button"
                    aria-label={t.admin.tsCopyUrl}
                    onclick={() => void copyUrl(ts.url)}>{t.admin.copy}</button
                  >
                  {#if copied}<span role="status">{t.admin.copied}</span>{/if}
                </dd>
              </div>
            {/if}
          </dl>
        {/if}

        {#if ts.cert_active}
          <p>
            {fmt(t.admin.tsCertActive, {
              name: ts.dns_name,
              date: formatDateTime(ts.cert_not_after),
              url: ts.url || `https://${ts.dns_name}/`,
            })}
            {#if ts.cert_not_after}
              <span class="chip" class:bad={daysUntil(ts.cert_not_after) < 30}
                >{daysUntil(ts.cert_not_after) < 0
                  ? t.admin.expired
                  : fmt(t.admin.daysLeft, { n: String(daysUntil(ts.cert_not_after)) })}</span
              >
            {/if}
          </p>
        {:else if ts.cert_enabled}
          <div class="callout" role="note">
            <strong>{t.admin.tsCertNotServed}</strong>
            {#if ts.cert_error}<p>{ts.cert_error}</p>{/if}
          </div>
        {:else}
          <p class="hint">{t.admin.tsCertOff}</p>
        {/if}

        {#if !byo && ts.dns_name && !ts.ca_covers_name && !ts.cert_active}
          <div class="callout" role="note">
            <p>
              {fmt(t.admin.tsCaNotCovering, {
                name: ts.dns_name,
                ip: tsIpv4 || t.admin.tsTailscaleAddress,
              })}
            </p>
          </div>
        {/if}
      </section>
    {/if}

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
  .plain {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .callout p {
    margin: var(--s1) 0 0;
  }
  .callout p:first-child {
    margin-top: 0;
  }
</style>
