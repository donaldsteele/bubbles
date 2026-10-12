<script lang="ts">
  import { onMount } from 'svelte';
  import Dialog from '../../components/a11y/Dialog.svelte';
  import QrImage from '../../components/common/QrImage.svelte';
  import SecretOnce from '../../components/common/SecretOnce.svelte';
  import { api, call, type Schemas } from '../../lib/api/client';
  import { formatDateTime } from '../../lib/admin/format';
  import FormField from '../../components/forms/FormField.svelte';
  import { failureFrom, validatePassword, validateRequired } from '../../lib/forms';
  import { fmt, t } from '../../lib/i18n/en';
  import { session } from '../../lib/state/session.svelte';

  type User = Schemas['User'];
  type Role = Schemas['Role'];

  let users = $state<User[]>([]);
  let loading = $state(true);
  let error = $state('');
  let link = $state<{ url: string; user: string; expires: string } | null>(null);
  let confirm = $state<{ user: User; action: 'deactivate' | 'sessions' } | null>(null);
  const roles: Role[] = ['admin', 'member', 'guest'];

  let addName = $state('');
  let addPassword = $state('');
  let addRole = $state<Role>('member');
  let addErrors = $state<Record<string, string>>({});
  let addError = $state('');
  let added = $state('');
  let adding = $state(false);

  async function load() {
    try {
      users = (await call(api.GET('/admin/users'))).items;
      error = '';
    } catch (err) {
      error = failureFrom(err).form;
    } finally {
      loading = false;
    }
  }
  onMount(() => void load());

  async function addPerson(e: SubmitEvent) {
    e.preventDefault();
    addErrors = { name: validateRequired(addName), password: validatePassword(addPassword) };
    addError = '';
    added = '';
    if (addErrors.name || addErrors.password) return;
    adding = true;
    try {
      const u = await call(
        api.POST('/admin/users', {
          body: { name: addName.trim(), password: addPassword, role: addRole },
        }),
      );
      added = fmt(t.admin.addedPerson, { name: u.display_name });
      addName = '';
      addPassword = '';
      addRole = 'member';
      await load();
    } catch (err) {
      const f = failureFrom(err);
      addErrors = f.fields['name'] ? { ...f.fields, name: t.admin.nameTaken } : f.fields;
      addError = f.form;
    } finally {
      adding = false;
    }
  }

  function replace(u: User) {
    users = users.map((x) => (x.id === u.id ? u : x));
  }

  async function patch(u: User, body: { role?: Role; deactivated?: boolean }) {
    error = '';
    try {
      replace(await call(api.PATCH('/admin/users/{id}', { params: { path: { id: u.id } }, body })));
    } catch (err) {
      error = failureFrom(err).form;
      await load();
    }
  }

  async function resetLink(u: User) {
    error = '';
    try {
      const r = await call(
        api.POST('/admin/users/{id}/password-reset', { params: { path: { id: u.id } } }),
      );
      link = { url: r.url, user: u.username, expires: r.expires_at };
    } catch (err) {
      error = failureFrom(err).form;
    }
  }

  async function runConfirmed() {
    const c = confirm;
    confirm = null;
    if (!c) return;
    if (c.action === 'deactivate') {
      await patch(c.user, { deactivated: true });
      return;
    }
    try {
      await call(api.DELETE('/admin/users/{id}/sessions', { params: { path: { id: c.user.id } } }));
    } catch (err) {
      error = failureFrom(err).form;
    }
  }
</script>

<section aria-labelledby="users-title">
  <h2 id="users-title">{t.admin.tabUsers}</h2>
  {#if error}<p class="form-error" role="alert">{error}</p>{/if}
  {#if loading}<p role="status">{t.app.loading}</p>{/if}

  {#if link}
    <SecretOnce
      title={fmt(t.admin.resetLinkFor, { user: link.user })}
      secret={link.url}
      ondismiss={() => (link = null)}
    >
      <p class="hint">{fmt(t.admin.resetLinkExpires, { when: formatDateTime(link.expires) })}</p>
      <QrImage value={link.url} label={t.admin.qrResetLink} />
    </SecretOnce>
  {/if}

  <form class="form panel" onsubmit={addPerson} novalidate>
    <h3>{t.admin.addPerson}</h3>
    <p class="hint">{t.admin.addPersonHint}</p>
    {#if addError}<p class="form-error" role="alert">{addError}</p>{/if}
    <FormField
      id="add-name"
      label={t.fields.yourName}
      bind:value={addName}
      error={addErrors['name']}
      autocomplete="off"
      required
      disabled={adding}
    />
    <FormField
      id="add-password"
      label={t.fields.password}
      type="password"
      bind:value={addPassword}
      error={addErrors['password']}
      autocomplete="new-password"
      required
      disabled={adding}
    />
    <label>
      {t.admin.role}
      <select bind:value={addRole} disabled={adding}>
        {#each ['member', 'guest', 'admin'] as r (r)}<option value={r}>{r}</option>{/each}
      </select>
    </label>
    <div class="row">
      <button class="btn primary" type="submit" disabled={adding}
        >{adding ? t.common.submitting : t.admin.addPerson}</button
      >
      {#if added}<span role="status">{added}</span>{/if}
    </div>
  </form>

  <div class="table-wrap">
    <table>
      <caption class="visually-hidden">{t.admin.tabUsers}</caption>
      <thead>
        <tr>
          <th scope="col">{t.fields.username}</th>
          <th scope="col">{t.admin.role}</th>
          <th scope="col">{t.admin.status}</th>
          <th scope="col">{t.admin.created}</th>
          <th scope="col">{t.admin.actions}</th>
        </tr>
      </thead>
      <tbody>
        {#each users as u (u.id)}
          {@const self = u.id === session.me?.id}
          <tr>
            <th scope="row">
              {u.display_name}
              <span class="hint">@{u.username}</span>
            </th>
            <td>
              <label class="visually-hidden" for={`role-${u.id}`}
                >{fmt(t.admin.roleOf, { user: u.username })}</label
              >
              <select
                id={`role-${u.id}`}
                value={u.role}
                disabled={self || u.deactivated}
                onchange={(e) => void patch(u, { role: e.currentTarget.value as Role })}
              >
                {#each roles as r (r)}<option value={r}>{r}</option>{/each}
              </select>
            </td>
            <td>
              {#if u.deactivated}<span class="chip bad">{t.admin.deactivated}</span>{:else}<span
                  class="chip ok">{t.admin.active}</span
                >{/if}
            </td>
            <td>{formatDateTime(u.created_at)}</td>
            <td>
              <div class="row">
                {#if u.deactivated}
                  <button
                    class="btn small"
                    type="button"
                    onclick={() => void patch(u, { deactivated: false })}
                    >{t.admin.reactivate}</button
                  >
                {:else}
                  <button class="btn small" type="button" onclick={() => void resetLink(u)}
                    >{t.admin.resetLink}</button
                  >
                  <button
                    class="btn small"
                    type="button"
                    onclick={() => (confirm = { user: u, action: 'sessions' })}
                    >{t.admin.revokeSessions}</button
                  >
                  <button
                    class="btn small"
                    type="button"
                    disabled={self}
                    onclick={() => (confirm = { user: u, action: 'deactivate' })}
                    >{t.admin.deactivate}</button
                  >
                {/if}
              </div>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</section>

{#if confirm}
  <Dialog
    alert
    title={confirm.action === 'deactivate' ? t.admin.deactivateTitle : t.admin.revokeSessionsTitle}
    onclose={() => (confirm = null)}
  >
    <div class="adm">
      <p>
        {fmt(
          confirm.action === 'deactivate' ? t.admin.deactivateBody : t.admin.revokeSessionsBody,
          {
            user: confirm.user.username,
          },
        )}
      </p>
      <div class="row">
        <button class="btn" type="button" data-autofocus onclick={() => (confirm = null)}
          >{t.common.cancel}</button
        >
        <button class="btn danger" type="button" onclick={() => void runConfirmed()}>
          {confirm.action === 'deactivate' ? t.admin.deactivate : t.admin.revokeSessions}
        </button>
      </div>
    </div>
  </Dialog>
{/if}
