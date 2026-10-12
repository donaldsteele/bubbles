<script lang="ts">
  import { onMount } from 'svelte';
  import { api, call, type Schemas } from '../../lib/api/client';
  import { formatDateTime } from '../../lib/admin/format';
  import { failureFrom } from '../../lib/forms';
  import { t } from '../../lib/i18n/en';

  type Entry = Schemas['AuditEntry'];

  const KNOWN = [
    'setup.complete',
    'auth.login',
    'auth.login_failed',
    'auth.logout',
    'auth.password_change',
    'auth.password_reset',
    'auth.reset_create',
    'invite.create',
    'invite.accept',
    'invite.revoke',
    'user.register',
    'user.update',
    'session.revoke',
    'settings.update',
    'channel.admin_update',
    'tls.reissue',
  ];

  let items = $state<Entry[]>([]);
  let next = $state<string | null>(null);
  let action = $state('');
  let loading = $state(true);
  let error = $state('');

  async function load(more: boolean) {
    loading = true;
    error = '';
    try {
      const page = await call(
        api.GET('/admin/audit', {
          params: {
            query: {
              ...(action.trim() ? { action: action.trim() } : {}),
              ...(more && next ? { cursor: next } : {}),
            },
          },
        }),
      );
      items = more ? [...items, ...page.items] : page.items;
      next = page.next_cursor;
    } catch (err) {
      error = failureFrom(err).form;
    } finally {
      loading = false;
    }
  }

  onMount(() => void load(false));

  function apply(e: SubmitEvent) {
    e.preventDefault();
    next = null;
    void load(false);
  }

  function detail(e: Entry): string {
    const d = JSON.stringify(e.detail);
    return d === '{}' ? '' : d;
  }
</script>

<section aria-labelledby="audit-title">
  <h2 id="audit-title">{t.admin.tabAudit}</h2>
  <form class="row" onsubmit={apply}>
    <label>
      {t.admin.auditAction}
      <input list="audit-actions" bind:value={action} maxlength="64" />
    </label>
    <datalist id="audit-actions">
      {#each KNOWN as a (a)}<option value={a}></option>{/each}
    </datalist>
    <button class="btn small" type="submit">{t.admin.filter}</button>
  </form>
  {#if error}<p class="form-error" role="alert">{error}</p>{/if}
  <div class="table-wrap">
    <table>
      <caption class="visually-hidden">{t.admin.tabAudit}</caption>
      <thead>
        <tr>
          <th scope="col">{t.admin.when}</th>
          <th scope="col">{t.admin.action}</th>
          <th scope="col">{t.admin.actor}</th>
          <th scope="col">{t.admin.target}</th>
          <th scope="col">{t.admin.detail}</th>
        </tr>
      </thead>
      <tbody>
        {#each items as e (e.id)}
          <tr>
            <td>{formatDateTime(e.at)}</td>
            <th scope="row" class="mono">{e.action}</th>
            <td>{e.actor_kind}{e.actor_ip ? ` · ${e.actor_ip}` : ''}</td>
            <td class="mono">{e.target_type}{e.target_id ? ` ${e.target_id}` : ''}</td>
            <td class="mono">{detail(e)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  {#if loading}<p role="status">{t.app.loading}</p>{/if}
  {#if !loading && items.length === 0}<p class="hint">{t.admin.noAudit}</p>{/if}
  {#if next}
    <button class="btn" type="button" disabled={loading} onclick={() => void load(true)}
      >{t.admin.loadMore}</button
    >
  {/if}
</section>
