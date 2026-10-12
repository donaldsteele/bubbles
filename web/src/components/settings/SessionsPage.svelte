<script lang="ts">
  import { onMount } from 'svelte';
  import { api, call } from '../../lib/api/client';
  import type { Schemas } from '../../lib/api/client';
  import { signOut } from '../../lib/boot';
  import { failureFrom, validatePassword, validateRequired } from '../../lib/forms';
  import { fmt, t } from '../../lib/i18n/en';
  import { relTime } from '../../lib/messages/time';
  import Dialog from '../a11y/Dialog.svelte';
  import FormField from '../forms/FormField.svelte';

  let sessions = $state<Schemas['Session'][]>([]);
  let loading = $state(true);
  let failed = $state(false);

  let devices = $state<Schemas['Device'][]>([]);
  let devicesLoading = $state(true);
  let devicesFailed = $state(false);
  let deviceError = $state('');
  let pendingDevice = $state<Schemas['Device'] | null>(null);
  let revoking = $state(false);

  let current = $state('');
  let next = $state('');
  let errors = $state<Record<string, string>>({});
  let formError = $state('');
  let changed = $state(false);
  let submitting = $state(false);

  async function load() {
    try {
      sessions = (await call(api.GET('/me/sessions'))).items;
    } catch {
      failed = true;
    } finally {
      loading = false;
    }
  }

  async function loadDevices() {
    try {
      devices = (await call(api.GET('/me/devices'))).items;
      devicesFailed = false;
    } catch {
      devicesFailed = true;
    } finally {
      devicesLoading = false;
    }
  }

  onMount(() => {
    void load();
    void loadDevices();
  });

  async function confirmRevokeDevice() {
    const d = pendingDevice;
    if (!d || revoking) return;
    revoking = true;
    deviceError = '';
    try {
      await call(api.DELETE('/me/devices/{id}', { params: { path: { id: d.id } } }));
      pendingDevice = null;
      await loadDevices();
    } catch (err) {
      pendingDevice = null;
      deviceError = failureFrom(err).form;
    } finally {
      revoking = false;
    }
  }

  async function revoke(s: Schemas['Session']) {
    try {
      await call(api.DELETE('/me/sessions/{id}', { params: { path: { id: s.id } } }));
    } catch {
      failed = true;
      return;
    }
    if (s.current) {
      await signOut();
      return;
    }
    sessions = sessions.filter((x) => x.id !== s.id);
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    errors = { current: validateRequired(current), new: validatePassword(next) };
    formError = '';
    changed = false;
    if (errors['current'] || errors['new']) return;
    submitting = true;
    try {
      await call(api.POST('/me/password', { body: { current, new: next } }));
      current = '';
      next = '';
      changed = true;
      await load();
    } catch (err) {
      const f = failureFrom(err, { 401: t.rich.wrongPassword, 403: t.rich.wrongPassword });
      errors = f.fields;
      formError = f.form;
    } finally {
      submitting = false;
    }
  }
</script>

<section aria-labelledby="sessions-title">
  <h2 id="sessions-title">{t.rich.sessionsTitle}</h2>
  {#if loading}
    <p role="status">{t.app.loading}</p>
  {:else if failed}
    <p class="form-error" role="alert">{t.chat.loadFailed}</p>
  {/if}
  <ul role="list">
    {#each sessions as s (s.id)}
      <li>
        <div class="info">
          <strong>
            {s.kind === 'handoff' ? t.rich.sessionHandoff : t.rich.sessionWeb}
            {#if s.current}<span class="chip">{t.rich.thisDevice}</span>{/if}
          </strong>
          <span class="ua">{s.user_agent}</span>
          <span class="meta"
            >{s.ip} · {fmt(t.rich.lastSeen, { when: relTime(s.last_seen_at) })}</span
          >
        </div>
        <button class="btn" type="button" onclick={() => revoke(s)}>{t.rich.revoke}</button>
      </li>
    {/each}
  </ul>

  <h2 id="devices-title">{t.rich.devicesTitle}</h2>
  {#if deviceError}<p class="form-error" role="alert">{deviceError}</p>{/if}
  {#if devicesLoading}
    <p role="status">{t.app.loading}</p>
  {:else if devicesFailed}
    <p class="form-error" role="alert">{t.rich.devicesLoadFailed}</p>
  {:else if devices.length === 0}
    <p class="meta">{t.rich.devicesEmpty}</p>
  {:else}
    <ul role="list" aria-labelledby="devices-title">
      {#each devices as d (d.id)}
        <li>
          <div class="info">
            <strong>{d.name}</strong>
            <span class="meta">{d.prefix}</span>
            <span class="meta"
              >{fmt(t.rich.deviceCreated, { when: new Date(d.created_at).toLocaleDateString() })}
              · {d.last_used_at
                ? fmt(t.rich.deviceLastUsed, { when: relTime(d.last_used_at) })
                : t.rich.deviceNeverUsed}</span
            >
          </div>
          <button
            class="btn"
            type="button"
            aria-label={`${t.rich.deviceSignOut}: ${d.name}`}
            onclick={() => (pendingDevice = d)}>{t.rich.deviceSignOut}</button
          >
        </li>
      {/each}
    </ul>
  {/if}

  {#if pendingDevice}
    {@const dev = pendingDevice}
    <Dialog
      alert
      title={t.rich.deviceSignOutTitle}
      onclose={() => {
        if (!revoking) pendingDevice = null;
      }}
    >
      <p>{fmt(t.rich.deviceSignOutBody, { name: dev.name })}</p>
      <div class="actions">
        <button
          class="btn"
          type="button"
          data-autofocus
          disabled={revoking}
          onclick={() => (pendingDevice = null)}>{t.common.cancel}</button
        >
        <button class="btn danger" type="button" disabled={revoking} onclick={confirmRevokeDevice}>
          {revoking ? t.common.submitting : t.rich.deviceSignOutConfirm}
        </button>
      </div>
    </Dialog>
  {/if}

  <h2>{t.rich.passwordTitle}</h2>
  <form onsubmit={submit} novalidate>
    {#if formError}<p class="form-error" role="alert">{formError}</p>{/if}
    <FormField
      id="pw-current"
      label={t.rich.currentPassword}
      type="password"
      bind:value={current}
      error={errors['current']}
      autocomplete="current-password"
      required
      disabled={submitting}
    />
    <FormField
      id="pw-new"
      label={t.rich.newPassword}
      type="password"
      bind:value={next}
      hint={t.hints.password}
      error={errors['new']}
      autocomplete="new-password"
      required
      disabled={submitting}
    />
    <button class="btn primary" type="submit" disabled={submitting}>
      {submitting ? t.common.submitting : t.rich.changePassword}
    </button>
    {#if changed}<p role="status">{t.rich.passwordChanged}</p>{/if}
  </form>
</section>

<style>
  h2 {
    font-size: 18px;
  }
  ul {
    list-style: none;
    margin: 0 0 var(--s5);
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
  .ua,
  .meta {
    color: var(--fg-muted);
    font-size: 12px;
    overflow-wrap: anywhere;
  }
  .actions {
    display: flex;
    gap: var(--s2);
    justify-content: flex-end;
    flex-wrap: wrap;
  }
  .danger {
    background: var(--bad);
    color: var(--bg);
    border-color: var(--bad);
    font-weight: 700;
  }
  .chip {
    margin-left: var(--s2);
    border: 1px solid var(--line-strong);
    border-radius: 999px;
    padding: 0 var(--s2);
    font-size: 11px;
  }
</style>
