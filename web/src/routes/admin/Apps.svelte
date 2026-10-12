<script lang="ts">
  import { onMount } from 'svelte';
  import Dialog from '../../components/a11y/Dialog.svelte';
  import ChannelPicker from '../../components/admin/ChannelPicker.svelte';
  import ScopePicker from '../../components/admin/ScopePicker.svelte';
  import QrImage from '../../components/common/QrImage.svelte';
  import SecretOnce from '../../components/common/SecretOnce.svelte';
  import FormField from '../../components/forms/FormField.svelte';
  import { api, call, type Schemas } from '../../lib/api/client';
  import { formatDateTime } from '../../lib/admin/format';
  import type { Scope } from '../../lib/admin/scopes';
  import { validateName, validateScopes } from '../../lib/admin/validate';
  import { failureFrom } from '../../lib/forms';
  import { fmt, t } from '../../lib/i18n/en';

  type App = Schemas['App'];

  let apps = $state<App[]>([]);
  let channels = $state<Schemas['Channel'][]>([]);
  let loading = $state(true);
  let error = $state('');

  let name = $state('');
  let description = $state('');
  let username = $state('');
  let errors = $state<Record<string, string>>({});
  let formError = $state('');
  let creating = $state(false);

  let tokenFor = $state<App | null>(null);
  let scopes = $state<Scope[]>([]);
  let allow = $state<string[]>([]);
  let expiresDays = $state('');
  let tokenErrors = $state<Record<string, string>>({});
  let tokenFormError = $state('');
  let issuing = $state(false);
  let issued = $state<{ token: string; connection: string; app: string } | null>(null);

  async function load() {
    try {
      const [a, c] = await Promise.all([
        call(api.GET('/admin/apps')),
        call(api.GET('/admin/channels')),
      ]);
      apps = a.items;
      channels = c.items;
      error = '';
    } catch (err) {
      error = failureFrom(err).form;
    } finally {
      loading = false;
    }
  }
  onMount(() => void load());

  async function createApp(e: SubmitEvent) {
    e.preventDefault();
    errors = {};
    const bad = validateName(name);
    if (bad) errors['name'] = bad;
    formError = '';
    if (bad) return;
    creating = true;
    try {
      await call(
        api.POST('/admin/apps', {
          body: {
            name: name.trim(),
            ...(description.trim() ? { description: description.trim() } : {}),
            ...(username.trim() ? { username: username.trim() } : {}),
          },
        }),
      );
      name = description = username = '';
      await load();
    } catch (err) {
      const f = failureFrom(err);
      errors = f.fields;
      formError = f.form;
    } finally {
      creating = false;
    }
  }

  async function setDisabled(app: App, disabled: boolean) {
    try {
      await call(
        api.PATCH('/admin/apps/{id}', { params: { path: { id: app.id } }, body: { disabled } }),
      );
      await load();
    } catch (err) {
      error = failureFrom(err).form;
    }
  }

  async function revoke(app: App, tokenId: string) {
    try {
      await call(
        api.DELETE('/admin/apps/{id}/tokens/{token_id}', {
          params: { path: { id: app.id, token_id: tokenId } },
        }),
      );
      await load();
    } catch (err) {
      error = failureFrom(err).form;
    }
  }

  function openToken(app: App) {
    tokenFor = app;
    scopes = [];
    allow = [];
    expiresDays = '';
    tokenErrors = {};
    tokenFormError = '';
  }

  async function issue(e: SubmitEvent) {
    e.preventDefault();
    const app = tokenFor;
    if (!app) return;
    tokenErrors = {};
    const sc = validateScopes(scopes);
    if (sc) tokenErrors['scopes'] = sc;
    let days = 0;
    if (expiresDays.trim() !== '') {
      days = /^\d{1,5}$/.test(expiresDays.trim()) ? Number(expiresDays.trim()) : 0;
      if (days < 1) tokenErrors['expires_at'] = t.admin.errTokenExpiry;
    }
    if (Object.keys(tokenErrors).length > 0) return;
    issuing = true;
    try {
      const r = await call(
        api.POST('/admin/apps/{id}/tokens', {
          params: { path: { id: app.id } },
          body: {
            scopes,
            channel_ids: allow.length > 0 ? allow : null,
            expires_at: days > 0 ? new Date(Date.now() + days * 86_400_000).toISOString() : null,
          },
        }),
      );
      issued = { token: r.token, connection: r.connection_string, app: app.name };
      tokenFor = null;
      await load();
    } catch (err) {
      const f = failureFrom(err);
      tokenErrors = f.fields;
      tokenFormError = f.form;
    } finally {
      issuing = false;
    }
  }
</script>

<section aria-labelledby="apps-title">
  <h2 id="apps-title">{t.admin.tabApps}</h2>
  <p class="lead">{t.admin.appsHelp}</p>
  {#if error}<p class="form-error" role="alert">{error}</p>{/if}

  {#if issued}
    <SecretOnce
      title={fmt(t.admin.tokenFor, { app: issued.app })}
      secret={issued.token}
      ondismiss={() => (issued = null)}
    >
      <p class="hint">{t.admin.connectionString}</p>
      <input
        class="mono"
        readonly
        value={issued.connection}
        aria-label={t.admin.connectionString}
        onfocus={(e) => e.currentTarget.select()}
      />
      <QrImage value={issued.connection} label={t.admin.qrConnection} />
    </SecretOnce>
  {/if}

  <form class="form panel" onsubmit={createApp} novalidate>
    <h3>{t.admin.newApp}</h3>
    {#if formError}<p class="form-error" role="alert">{formError}</p>{/if}
    <FormField
      id="app-name"
      label={t.admin.appName}
      bind:value={name}
      error={errors['name']}
      required
      disabled={creating}
    />
    <FormField
      id="app-desc"
      label={t.admin.description}
      bind:value={description}
      error={errors['description']}
      disabled={creating}
    />
    <FormField
      id="app-user"
      label={t.admin.botUsername}
      bind:value={username}
      error={errors['username']}
      hint={t.admin.botUsernameHint}
      disabled={creating}
    />
    <div>
      <button class="btn primary" type="submit" disabled={creating}
        >{creating ? t.common.submitting : t.admin.createApp}</button
      >
    </div>
  </form>

  {#if loading}<p role="status">{t.app.loading}</p>{/if}
  {#each apps as app (app.id)}
    <article class="panel app" aria-labelledby={`app-${app.id}`}>
      <header class="row">
        <h3 id={`app-${app.id}`}>{app.name} <span class="hint">@{app.username}</span></h3>
        {#if app.disabled_at}<span class="chip bad">{t.admin.disabled}</span>{/if}
        <span class="spacer"></span>
        <button class="btn small" type="button" onclick={() => openToken(app)}
          >{t.admin.newToken}</button
        >
        <button
          class="btn small"
          type="button"
          onclick={() => void setDisabled(app, !app.disabled_at)}
        >
          {app.disabled_at ? t.admin.enable : t.admin.disable}
        </button>
      </header>
      {#if app.description}<p class="hint">{app.description}</p>{/if}
      {#if app.tokens.length === 0}
        <p class="hint">{t.admin.noTokens}</p>
      {:else}
        <div class="table-wrap">
          <table>
            <caption class="visually-hidden">{fmt(t.admin.tokensOf, { app: app.name })}</caption>
            <thead>
              <tr>
                <th scope="col">{t.admin.token}</th>
                <th scope="col">{t.admin.scopes}</th>
                <th scope="col">{t.admin.channels}</th>
                <th scope="col">{t.admin.expires}</th>
                <th scope="col">{t.admin.lastUsed}</th>
                <th scope="col">{t.admin.actions}</th>
              </tr>
            </thead>
            <tbody>
              {#each app.tokens as tok (tok.id)}
                <tr>
                  <th scope="row" class="mono">{tok.prefix}…</th>
                  <td>{tok.scopes.join(', ')}</td>
                  <td
                    >{tok.channel_ids === null
                      ? t.admin.allMemberChannels
                      : tok.channel_ids.length}</td
                  >
                  <td>{tok.expires_at ? formatDateTime(tok.expires_at) : t.admin.never}</td>
                  <td>{tok.last_used_at ? formatDateTime(tok.last_used_at) : t.admin.never}</td>
                  <td>
                    {#if tok.revoked_at}
                      <span class="chip bad">{t.admin.revoked}</span>
                    {:else}
                      <button
                        class="btn small"
                        type="button"
                        onclick={() => void revoke(app, tok.id)}>{t.admin.revoke}</button
                      >
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </article>
  {/each}
</section>

{#if tokenFor}
  <Dialog
    title={fmt(t.admin.newTokenFor, { app: tokenFor.name })}
    onclose={() => (tokenFor = null)}
  >
    <form class="form" onsubmit={issue} novalidate>
      {#if tokenFormError}<p class="form-error" role="alert">{tokenFormError}</p>{/if}
      <ScopePicker bind:scopes error={tokenErrors['scopes']} disabled={issuing} />
      <ChannelPicker {channels} bind:selected={allow} disabled={issuing} />
      <FormField
        id="tok-exp"
        label={t.admin.tokenExpiresDays}
        bind:value={expiresDays}
        error={tokenErrors['expires_at']}
        hint={t.admin.tokenExpiresHint}
        disabled={issuing}
      />
      <div class="row">
        <button class="btn" type="button" onclick={() => (tokenFor = null)}
          >{t.common.cancel}</button
        >
        <button class="btn primary" type="submit" disabled={issuing}
          >{issuing ? t.common.submitting : t.admin.issueToken}</button
        >
      </div>
    </form>
  </Dialog>
{/if}

<style>
  .app {
    margin: var(--s3) 0;
  }
  h3 {
    margin: 0;
    font-size: 16px;
  }
  .spacer {
    flex: 1;
  }
</style>
