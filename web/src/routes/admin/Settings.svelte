<script lang="ts">
  import { onMount } from 'svelte';
  import FormField from '../../components/forms/FormField.svelte';
  import { api, call, type Schemas } from '../../lib/api/client';
  import { formatDateTime } from '../../lib/admin/format';
  import {
    settingsBody,
    settingsToForm,
    validateSettings,
    type SettingsForm,
  } from '../../lib/admin/validate';
  import { failureFrom } from '../../lib/forms';
  import { fmt, t } from '../../lib/i18n/en';

  let form = $state<SettingsForm | null>(null);
  let errors = $state<Record<string, string>>({});
  let formError = $state('');
  let saved = $state(false);
  let saving = $state(false);
  // Sign-up state the server computes; it is not part of the editable form.
  let regOpen = $state(false);
  let regUntil = $state<string | null>(null);
  let savedWindow = $state(0);
  let regBusy = $state(false);

  const WINDOWS = [
    { hours: 1, label: t.admin.window1h },
    { hours: 4, label: t.admin.window4h },
    { hours: 24, label: t.admin.window1d },
    { hours: 72, label: t.admin.window3d },
    { hours: 0, label: t.admin.windowNever },
  ];

  function windowLabel(hours: number): string {
    return (
      WINDOWS.find((w) => w.hours === hours)?.label ??
      fmt(t.admin.windowHours, { n: String(hours) })
    );
  }

  const signupStatus = $derived(
    regOpen && regUntil
      ? fmt(t.admin.signupOpenUntil, { time: formatDateTime(regUntil) })
      : regOpen
        ? t.admin.signupOpenForever
        : t.admin.signupClosed,
  );

  function adoptRegistration(s: Schemas['AdminSettings']) {
    regOpen = s.registration_open;
    regUntil = s.open_registration_until;
    savedWindow = s.registration_window_hours;
  }

  /** Reopen or close sign-up right away; only the sign-up fields of the form change. */
  async function setRegistration(action: 'reopen' | 'close') {
    if (!form) return;
    regBusy = true;
    saved = false;
    formError = '';
    try {
      if (action === 'reopen' && form.registrationWindowHours !== savedWindow) {
        await call(
          api.PATCH('/admin/settings', {
            body: { registration_window_hours: form.registrationWindowHours },
          }),
        );
      }
      const s = await call(api.POST('/admin/registration', { body: { action } }));
      form.openRegistration = s.open_registration;
      form.registrationWindowHours = s.registration_window_hours;
      adoptRegistration(s);
    } catch (err) {
      formError = failureFrom(err).form;
    } finally {
      regBusy = false;
    }
  }

  onMount(() => {
    void call(api.GET('/admin/settings')).then(
      (s) => {
        form = settingsToForm(s);
        adoptRegistration(s);
      },
      (err: unknown) => (formError = failureFrom(err).form),
    );
  });

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!form) return;
    errors = validateSettings(form);
    saved = false;
    formError = '';
    if (Object.keys(errors).length > 0) return;
    saving = true;
    try {
      const s = await call(api.PATCH('/admin/settings', { body: settingsBody(form) }));
      form = settingsToForm(s);
      adoptRegistration(s);
      saved = true;
    } catch (err) {
      const f = failureFrom(err);
      errors = f.fields;
      formError = f.form;
    } finally {
      saving = false;
    }
  }
</script>

<section aria-labelledby="set-title">
  <h2 id="set-title">{t.admin.tabSettings}</h2>
  {#if formError}<p class="form-error" role="alert">{formError}</p>{/if}
  {#if !form && !formError}<p role="status">{t.app.loading}</p>{/if}
  {#if form}
    <form class="form" onsubmit={submit} novalidate>
      <FormField
        id="set-name"
        label={t.fields.instanceName}
        bind:value={form.instanceName}
        error={errors['instance_name']}
        required
        disabled={saving}
      />

      <fieldset>
        <legend>{t.admin.settingsSignup}</legend>
        <p role="status">{signupStatus}</p>
        <label class="inline"
          ><input type="checkbox" bind:checked={form.openRegistration} disabled={saving} />
          {t.admin.openRegistration}</label
        >
        <p class="hint">{t.admin.openRegistrationHint}</p>
        <label>
          {t.admin.signupWindow}
          <select id="set-window" bind:value={form.registrationWindowHours} disabled={saving}>
            {#if !WINDOWS.some((w) => w.hours === form?.registrationWindowHours)}
              <option value={form.registrationWindowHours}
                >{windowLabel(form.registrationWindowHours)}</option
              >
            {/if}
            {#each WINDOWS as w (w.hours)}<option value={w.hours}>{w.label}</option>{/each}
          </select>
        </label>
        {#if errors['registration_window_hours']}<p class="field-error" role="alert">
            {errors['registration_window_hours']}
          </p>{/if}
        <div class="row">
          <button
            class="btn"
            type="button"
            disabled={saving || regBusy}
            onclick={() => void setRegistration('reopen')}
            >{form.registrationWindowHours === 0
              ? t.admin.reopen
              : fmt(t.admin.reopenFor, {
                  length: windowLabel(form.registrationWindowHours),
                })}</button
          >
          <button
            class="btn"
            type="button"
            disabled={saving || regBusy || !regOpen}
            onclick={() => void setRegistration('close')}>{t.admin.closeNow}</button
          >
        </div>
      </fieldset>

      <fieldset>
        <legend>{t.admin.settingsAccess}</legend>
        <FormField
          id="set-cidrs-ta"
          label={t.admin.allowedCidrs}
          bind:value={form.allowedCidrs}
          error={errors['allowed_cidrs']}
          hint={t.admin.allowedCidrsHint}
          disabled={saving}
        />
        <p class="hint">{t.admin.allowedCidrsCaution}</p>
        <FormField
          id="set-idle"
          label={t.admin.sessionIdleDays}
          bind:value={form.sessionIdleDays}
          error={errors['session_idle_days']}
          disabled={saving}
        />
      </fieldset>

      <fieldset>
        <legend>{t.admin.settingsFiles}</legend>
        <FormField
          id="set-upload"
          label={t.admin.uploadMaxMB}
          bind:value={form.uploadMaxMB}
          error={errors['upload_max_bytes']}
          disabled={saving}
        />
        <FormField
          id="set-quota"
          label={t.admin.userQuotaMB}
          bind:value={form.userQuotaMB}
          error={errors['user_quota_bytes']}
          hint={t.admin.userQuotaHint}
          disabled={saving}
        />
        <FormField
          id="set-reserve"
          label={t.admin.diskReserveMB}
          bind:value={form.diskReserveMB}
          error={errors['disk_reserve_bytes']}
          hint={t.admin.diskReserveHint}
          disabled={saving}
        />
        <FormField
          id="set-retention"
          label={t.admin.defaultRetention}
          bind:value={form.retentionDays}
          error={errors['default_retention_days']}
          hint={t.admin.defaultRetentionHint}
          disabled={saving}
        />
      </fieldset>

      <fieldset>
        <legend>{t.admin.settingsNotify}</legend>
        <label class="inline"
          ><input type="checkbox" bind:checked={form.webPushEnabled} disabled={saving} />
          {t.admin.webPushEnabled}</label
        >
        <p class="hint">{t.admin.webPushHint}</p>
        <label>
          {t.admin.pushPayload}
          <select bind:value={form.pushPayload} disabled={saving}>
            <option value="minimal">{t.admin.pushMinimal}</option>
            <option value="full">{t.admin.pushFull}</option>
          </select>
        </label>
        {#if errors['push_payload']}<p class="field-error" role="alert">
            {errors['push_payload']}
          </p>{/if}
        <FormField
          id="set-renotify"
          label={t.admin.alertRenotifyS}
          bind:value={form.alertRenotifyS}
          error={errors['alert_renotify_s']}
          disabled={saving}
        />
      </fieldset>

      <fieldset>
        <legend>{t.admin.settingsServer}</legend>
        <label class="inline"
          ><input type="checkbox" bind:checked={form.mdnsEnabled} disabled={saving} />
          {t.admin.mdnsEnabled}</label
        >
        <label class="inline"
          ><input type="checkbox" bind:checked={form.webResetEnabled} disabled={saving} />
          {t.admin.webResetEnabled}</label
        >
        <p class="hint">{t.admin.webResetHint}</p>
      </fieldset>

      <div class="row">
        <button class="btn primary" type="submit" disabled={saving}
          >{saving ? t.common.submitting : t.common.save}</button
        >
        {#if saved}<span role="status">{t.admin.saved}</span>{/if}
      </div>
    </form>
  {/if}
</section>
