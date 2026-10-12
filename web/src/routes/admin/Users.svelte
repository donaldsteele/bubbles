<script lang="ts">
  import { onMount } from 'svelte';
  import Dialog from '../../components/a11y/Dialog.svelte';
  import QrImage from '../../components/common/QrImage.svelte';
  import SecretOnce from '../../components/common/SecretOnce.svelte';
  import { api, call, type Schemas } from '../../lib/api/client';
  import { formatDateTime } from '../../lib/admin/format';
  import { failureFrom } from '../../lib/forms';
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
