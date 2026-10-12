<script lang="ts">
  import { onMount } from 'svelte';
  import QrImage from '../../components/common/QrImage.svelte';
  import SecretOnce from '../../components/common/SecretOnce.svelte';
  import FormField from '../../components/forms/FormField.svelte';
  import { api, call, type Schemas } from '../../lib/api/client';
  import { formatDateTime } from '../../lib/admin/format';
  import { validateName } from '../../lib/admin/validate';
  import { failureFrom } from '../../lib/forms';
  import { t } from '../../lib/i18n/en';

  let hooks = $state<Schemas['Webhook'][]>([]);
  let channels = $state<Schemas['Channel'][]>([]);
  let loading = $state(true);
  let error = $state('');
  let channelId = $state('');
  let name = $state('');
  let errors = $state<Record<string, string>>({});
  let formError = $state('');
  let submitting = $state(false);
  let created = $state<string | null>(null);

  const channelName = (id: string) => channels.find((c) => c.id === id)?.name ?? id;

  async function load() {
    try {
      const [h, c] = await Promise.all([
        call(api.GET('/admin/webhooks')),
        call(api.GET('/admin/channels')),
      ]);
      hooks = h.items;
      channels = c.items;
      if (!channelId) channelId = c.items.find((x) => !x.archived_at)?.id ?? '';
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
    errors = {};
    const bad = validateName(name);
    if (bad) errors['name'] = bad;
    if (!channelId) errors['channel_id'] = t.common.required;
    formError = '';
    if (Object.keys(errors).length > 0) return;
    submitting = true;
    try {
      const w = await call(
        api.POST('/admin/webhooks', { body: { channel_id: channelId, name: name.trim() } }),
      );
      created = w.url;
      name = '';
      await load();
    } catch (err) {
      const f = failureFrom(err);
      errors = f.fields;
      formError = f.form;
    } finally {
      submitting = false;
    }
  }

  async function revoke(id: string) {
    try {
      await call(api.DELETE('/admin/webhooks/{id}', { params: { path: { id } } }));
      await load();
    } catch (err) {
      error = failureFrom(err).form;
    }
  }
</script>

<section aria-labelledby="hook-title">
  <h2 id="hook-title">{t.admin.tabWebhooks}</h2>
  <p class="lead">{t.admin.webhooksHelp}</p>
  {#if error}<p class="form-error" role="alert">{error}</p>{/if}

  {#if created}
    <SecretOnce title={t.admin.webhookUrl} secret={created} ondismiss={() => (created = null)}>
      <QrImage value={created} label={t.admin.qrWebhook} />
    </SecretOnce>
  {/if}

  <form class="form panel" onsubmit={submit} novalidate>
    <h3>{t.admin.newWebhook}</h3>
    {#if formError}<p class="form-error" role="alert">{formError}</p>{/if}
    <label>
      {t.admin.channel}
      <select
        bind:value={channelId}
        disabled={submitting}
        aria-invalid={errors['channel_id'] ? 'true' : undefined}
      >
        {#each channels.filter((c) => !c.archived_at) as c (c.id)}<option value={c.id}
            >#{c.name}</option
          >{/each}
      </select>
    </label>
    {#if errors['channel_id']}<p class="field-error" role="alert">{errors['channel_id']}</p>{/if}
    <FormField
      id="hook-name"
      label={t.admin.webhookName}
      bind:value={name}
      error={errors['name']}
      hint={t.admin.webhookNameHint}
      disabled={submitting}
    />
    <div>
      <button class="btn primary" type="submit" disabled={submitting}
        >{submitting ? t.common.submitting : t.admin.createWebhook}</button
      >
    </div>
  </form>

  {#if loading}<p role="status">{t.app.loading}</p>{/if}
  <div class="table-wrap">
    <table>
      <caption class="visually-hidden">{t.admin.tabWebhooks}</caption>
      <thead>
        <tr>
          <th scope="col">{t.admin.webhookName}</th>
          <th scope="col">{t.admin.channel}</th>
          <th scope="col">{t.admin.created}</th>
          <th scope="col">{t.admin.actions}</th>
        </tr>
      </thead>
      <tbody>
        {#each hooks as w (w.id)}
          <tr>
            <th scope="row">{w.name}</th>
            <td>#{channelName(w.channel_id)}</td>
            <td>{formatDateTime(w.created_at)}</td>
            <td>
              {#if w.revoked_at}
                <span class="chip bad">{t.admin.revoked}</span>
              {:else}
                <button class="btn small" type="button" onclick={() => void revoke(w.id)}
                  >{t.admin.revoke}</button
                >
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</section>
