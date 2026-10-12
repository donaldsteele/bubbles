<script lang="ts">
  import { onMount } from 'svelte';
  import QrImage from '../../components/common/QrImage.svelte';
  import SecretOnce from '../../components/common/SecretOnce.svelte';
  import FormField from '../../components/forms/FormField.svelte';
  import { api, call, type Schemas } from '../../lib/api/client';
  import { formatDateTime } from '../../lib/admin/format';
  import { validateInvite, type InviteForm } from '../../lib/admin/validate';
  import { failureFrom } from '../../lib/forms';
  import { t } from '../../lib/i18n/en';

  type Invite = Schemas['AdminInvite'];

  let invites = $state<Invite[]>([]);
  let loading = $state(true);
  let error = $state('');
  let created = $state<string | null>(null);
  let form = $state<InviteForm>({ role: 'member', expiresHours: '72', maxUses: '1', note: '' });
  let errors = $state<Record<string, string>>({});
  let formError = $state('');
  let submitting = $state(false);

  async function load() {
    try {
      invites = (await call(api.GET('/admin/invites'))).items;
      error = '';
    } catch (err) {
      error = failureFrom(err).form;
    } finally {
      loading = false;
    }
  }
  onMount(() => void load());

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    errors = validateInvite(form);
    formError = '';
    if (Object.keys(errors).length > 0) return;
    submitting = true;
    try {
      const inv = await call(
        api.POST('/admin/invites', {
          body: {
            role: form.role,
            note: form.note.trim(),
            expires_in_seconds: Number(form.expiresHours) * 3600,
            max_uses: Number(form.maxUses),
          },
        }),
      );
      created = inv.url;
      await load();
    } catch (err) {
      const f = failureFrom(err);
      errors = f.fields;
      formError = f.form;
    } finally {
      submitting = false;
    }
  }

  async function revoke(inv: Invite) {
    try {
      await call(api.DELETE('/admin/invites/{id}', { params: { path: { id: inv.id } } }));
      await load();
    } catch (err) {
      error = failureFrom(err).form;
    }
  }

  function statusOf(inv: Invite): string {
    if (inv.revoked_at) return t.admin.revoked;
    if (inv.use_count >= inv.max_uses) return t.admin.usedUp;
    if (new Date(inv.expires_at).getTime() < Date.now()) return t.admin.expired;
    return t.admin.active;
  }
</script>

<section aria-labelledby="inv-title">
  <h2 id="inv-title">{t.admin.tabInvites}</h2>
  {#if error}<p class="form-error" role="alert">{error}</p>{/if}

  {#if created}
    <SecretOnce title={t.admin.inviteLink} secret={created} ondismiss={() => (created = null)}>
      <QrImage value={created} label={t.admin.qrInviteLink} />
    </SecretOnce>
  {/if}

  <form class="form panel" onsubmit={submit} novalidate>
    <h3>{t.admin.newInvite}</h3>
    {#if formError}<p class="form-error" role="alert">{formError}</p>{/if}
    <label>
      {t.admin.role}
      <select bind:value={form.role} disabled={submitting}>
        <option value="member">member</option>
        <option value="guest">guest</option>
        <option value="admin">admin</option>
      </select>
    </label>
    <FormField
      id="inv-exp"
      label={t.admin.expiresHours}
      bind:value={form.expiresHours}
      error={errors['expires_in']}
      hint={t.admin.expiresHint}
      disabled={submitting}
    />
    <FormField
      id="inv-uses"
      label={t.admin.maxUses}
      bind:value={form.maxUses}
      error={errors['max_uses']}
      hint={t.admin.maxUsesHint}
      disabled={submitting}
    />
    <FormField
      id="inv-note"
      label={t.admin.note}
      bind:value={form.note}
      error={errors['note']}
      disabled={submitting}
    />
    <div>
      <button class="btn primary" type="submit" disabled={submitting}
        >{submitting ? t.common.submitting : t.admin.createInvite}</button
      >
    </div>
  </form>

  {#if loading}<p role="status">{t.app.loading}</p>{/if}
  <div class="table-wrap">
    <table>
      <caption class="visually-hidden">{t.admin.tabInvites}</caption>
      <thead>
        <tr>
          <th scope="col">{t.admin.note}</th>
          <th scope="col">{t.admin.role}</th>
          <th scope="col">{t.admin.uses}</th>
          <th scope="col">{t.admin.expires}</th>
          <th scope="col">{t.admin.status}</th>
          <th scope="col">{t.admin.actions}</th>
        </tr>
      </thead>
      <tbody>
        {#each invites as inv (inv.id)}
          <tr>
            <th scope="row">{inv.note || t.admin.none}</th>
            <td>{inv.role}</td>
            <td>{inv.use_count} / {inv.max_uses}</td>
            <td>{formatDateTime(inv.expires_at)}</td>
            <td
              ><span class="chip" class:ok={statusOf(inv) === t.admin.active}>{statusOf(inv)}</span
              ></td
            >
            <td>
              <button
                class="btn small"
                type="button"
                disabled={!!inv.revoked_at}
                onclick={() => void revoke(inv)}>{t.admin.revoke}</button
              >
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</section>
