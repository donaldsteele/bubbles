<script lang="ts">
  import { onMount } from 'svelte';
  import ChannelPicker from '../../components/admin/ChannelPicker.svelte';
  import ScopePicker from '../../components/admin/ScopePicker.svelte';
  import { api, call, type Schemas } from '../../lib/api/client';
  import { formatDateTime, groupFingerprint } from '../../lib/admin/format';
  import {
    alertsBotWithoutChannel,
    createChannelByDefault,
    joinableChannels,
  } from '../../lib/admin/pairing';
  import type { Scope } from '../../lib/admin/scopes';
  import { validateScopes } from '../../lib/admin/validate';
  import { failureFrom } from '../../lib/forms';
  import { fmt, t } from '../../lib/i18n/en';

  type Pending = Schemas['PendingPairing'];

  const POLL_MS = 5000;

  let items = $state<Pending[]>([]);
  let channels = $state<Schemas['Channel'][]>([]);
  let loading = $state(true);
  let error = $state('');
  let approving = $state<Pending | null>(null);
  let scopes = $state<Scope[]>([]);
  let picked = $state<string[]>([]);
  let createChannel = $state(false);
  // Reconnect the existing app the request names instead of creating one.
  let replaceApp = $state(false);
  let scopeError = $state('');
  let busy = $state(false);
  let done = $state('');
  let joinable = $derived(joinableChannels(channels));
  let replacing = $derived(replaceApp ? (approving?.replaces ?? null) : null);
  let noChannel = $derived(
    alertsBotWithoutChannel(scopes, picked, channels, createChannel, replacing),
  );

  async function load() {
    try {
      items = (await call(api.GET('/admin/pairing-requests'))).items;
      error = '';
    } catch (err) {
      error = failureFrom(err).form;
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    void load();
    void call(api.GET('/admin/channels')).then(
      (c) => (channels = c.items),
      () => {},
    );
    const timer = setInterval(() => void load(), POLL_MS);
    return () => clearInterval(timer);
  });

  function startApprove(p: Pending) {
    approving = p;
    scopes = [...p.requested_scopes];
    picked = [];
    setReplace(p, p.replaces !== undefined);
    scopeError = '';
    done = '';
  }

  // Choosing between a new app and the existing one changes which alerts
  // channel the server would use and whether the bot already has a channel.
  function setReplace(p: Pending, replace: boolean) {
    replaceApp = replace && p.replaces !== undefined;
    const existing = replaceApp ? (p.replaces ?? null) : null;
    createChannel = createChannelByDefault(p.requested_scopes, p.app_name, channels, existing);
  }

  function replacesLine(r: NonNullable<Pending['replaces']>): string {
    const who = fmt(r.disabled_at ? t.admin.pairingReplacesDisabled : t.admin.pairingReplaces, {
      name: r.name,
      username: r.username,
    });
    const where =
      r.channels.length > 0
        ? fmt(t.admin.pairingReplacesIn, {
            channels: r.channels.map((c) => '#' + c.name).join(', '),
          })
        : t.admin.pairingReplacesInNone;
    return who + '. ' + where;
  }

  async function approve(e: SubmitEvent) {
    e.preventDefault();
    const p = approving;
    if (!p) return;
    scopeError = validateScopes(scopes);
    if (scopeError) return;
    busy = true;
    try {
      const r = await call(
        api.POST('/admin/pairing-requests/{id}/approve', {
          params: { path: { id: p.id } },
          body: {
            scopes,
            channel_ids: picked,
            create_channel: createChannel,
            ...(p.replaces ? { replace_app: replaceApp } : {}),
          },
        }),
      );
      done = fmt(t.admin.pairingApproved, { app: r.app.name });
      approving = null;
      await load();
    } catch (err) {
      const f = failureFrom(err);
      scopeError = f.fields['scopes'] ?? f.form;
    } finally {
      busy = false;
    }
  }

  async function deny(p: Pending) {
    try {
      await call(api.POST('/admin/pairing-requests/{id}/deny', { params: { path: { id: p.id } } }));
      if (approving?.id === p.id) approving = null;
      await load();
    } catch (err) {
      error = failureFrom(err).form;
    }
  }
</script>

<section aria-labelledby="pair-title">
  <h2 id="pair-title">{t.admin.tabPairing}</h2>
  <p class="lead">{t.admin.pairingHelp}</p>
  {#if error}<p class="form-error" role="alert">{error}</p>{/if}
  {#if done}<p role="status">{done}</p>{/if}
  {#if loading}<p role="status">{t.app.loading}</p>{/if}
  {#if !loading && items.length === 0}<p class="hint">{t.admin.noPairing}</p>{/if}

  {#each items as p (p.id)}
    <article class="panel req" aria-labelledby={`pr-${p.id}`}>
      <h3 id={`pr-${p.id}`}>{p.app_name}</h3>
      {#if p.description}<p>{p.description}</p>{/if}
      {#if p.replaces}<p class="replaces">{replacesLine(p.replaces)}</p>{/if}
      <dl>
        <div>
          <dt>{t.admin.pairingCode}</dt>
          <dd class="code">{p.code}</dd>
        </div>
        <div>
          <dt>{t.admin.sourceIp}</dt>
          <dd class="mono">{p.source_ip}</dd>
        </div>
        <div>
          <dt>{t.admin.caFingerprint}</dt>
          <dd class="mono">{groupFingerprint(p.ca_fingerprint)}</dd>
        </div>
        <div>
          <dt>{t.admin.requestedScopes}</dt>
          <dd>{p.requested_scopes.join(', ') || t.admin.none}</dd>
        </div>
        <div>
          <dt>{t.admin.expires}</dt>
          <dd>{formatDateTime(p.expires_at)}</dd>
        </div>
      </dl>
      <p class="hint">{t.admin.pairingCompare}</p>
      <div class="row">
        <button class="btn primary small" type="button" onclick={() => startApprove(p)}
          >{t.admin.approve}</button
        >
        <button class="btn small" type="button" onclick={() => void deny(p)}>{t.admin.deny}</button>
      </div>
      {#if approving?.id === p.id}
        <form class="form" onsubmit={approve} novalidate>
          {#if p.replaces}
            <div>
              <label class="inline">
                <input
                  type="checkbox"
                  checked={replaceApp}
                  disabled={busy}
                  aria-describedby={`pair-${p.id}-reconnect`}
                  onchange={(e) => setReplace(p, e.currentTarget.checked)}
                />
                {t.admin.pairingReconnect}
              </label>
              <p id={`pair-${p.id}-reconnect`} class="hint">
                {t.admin.pairingReconnectHelp}
                {#if p.replaces.disabled_at}{t.admin.pairingReconnectEnables}{/if}
              </p>
            </div>
          {/if}
          <ScopePicker bind:scopes error={scopeError} idPrefix={`pair-${p.id}`} disabled={busy} />
          <ChannelPicker
            channels={joinable}
            bind:selected={picked}
            legend={t.admin.pairingChannels}
            help={t.admin.pairingChannelsHelp}
            disabled={busy}
          />
          <label class="inline">
            <input type="checkbox" bind:checked={createChannel} disabled={busy} />
            {t.admin.createChannelFor}
          </label>
          <div class="row">
            <button class="btn" type="button" onclick={() => (approving = null)}
              >{t.common.cancel}</button
            >
            <button
              class="btn primary"
              type="submit"
              disabled={busy}
              aria-describedby={noChannel ? `pair-${p.id}-nochan` : undefined}
              >{busy ? t.common.submitting : t.admin.approve}</button
            >
            {#if noChannel}
              <p id={`pair-${p.id}-nochan`} class="warn" role="note">{t.admin.pairingNoChannel}</p>
            {/if}
          </div>
        </form>
      {/if}
    </article>
  {/each}
</section>

<style>
  .req {
    margin: var(--s3) 0;
  }
  h3 {
    margin: 0 0 var(--s2);
    font-size: 16px;
  }
  dl {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: var(--s2);
    margin: 0 0 var(--s2);
  }
  dt {
    color: var(--fg-muted);
    font-size: 13px;
  }
  dd {
    margin: 0;
  }
  .replaces {
    font-weight: 600;
  }
  .warn {
    margin: 0;
    color: var(--warn);
    font-size: 13px;
  }
  .code {
    font-family: var(--font-mono);
    font-size: 28px;
    letter-spacing: 0.15em;
    font-weight: 700;
  }
</style>
