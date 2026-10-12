<script lang="ts">
  import { api, call } from '../lib/api/client';
  import { completeSignIn } from '../lib/boot';
  import { failureFrom, validatePassword, validateRequired, validateUsername } from '../lib/forms';
  import { t } from '../lib/i18n/en';
  import FormField from '../components/forms/FormField.svelte';

  let setupToken = $state('');
  let username = $state('');
  let displayName = $state('');
  let password = $state('');
  let instanceName = $state('');
  let errors = $state<Record<string, string>>({});
  let formError = $state('');
  let submitting = $state(false);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    errors = {
      setup_token: validateRequired(setupToken),
      username: validateUsername(username),
      password: validatePassword(password),
      instance_name: validateRequired(instanceName),
    };
    formError = '';
    if (Object.values(errors).some(Boolean)) return;
    submitting = true;
    try {
      const body = {
        setup_token: setupToken.trim(),
        username: username.trim().toLowerCase(),
        password,
        instance_name: instanceName.trim(),
        ...(displayName.trim() ? { display_name: displayName.trim() } : {}),
      };
      const res = await call(api.POST('/setup', { body }));
      await completeSignIn(res.user);
    } catch (err) {
      const f = failureFrom(err, { 403: t.setup.forbidden, 409: t.setup.conflict });
      errors = f.fields;
      formError = f.form;
    } finally {
      submitting = false;
    }
  }
</script>

<main class="card">
  <h1>{t.setup.title}</h1>
  <p>{t.setup.intro}</p>
  <form onsubmit={submit} novalidate>
    {#if formError}<p class="form-error" role="alert">{formError}</p>{/if}
    <FormField
      id="setup-token"
      label={t.fields.setupToken}
      bind:value={setupToken}
      hint={t.hints.setupToken}
      error={errors.setup_token}
      autocomplete="off"
      required
      disabled={submitting}
    />
    <FormField
      id="setup-instance"
      label={t.fields.instanceName}
      bind:value={instanceName}
      error={errors.instance_name}
      required
      disabled={submitting}
    />
    <FormField
      id="setup-username"
      label={t.fields.username}
      bind:value={username}
      hint={t.hints.username}
      error={errors.username}
      autocomplete="username"
      required
      disabled={submitting}
    />
    <FormField
      id="setup-display"
      label={t.fields.displayName}
      bind:value={displayName}
      hint={t.hints.displayName}
      error={errors.display_name}
      disabled={submitting}
    />
    <FormField
      id="setup-password"
      label={t.fields.password}
      type="password"
      bind:value={password}
      hint={t.hints.password}
      error={errors.password}
      autocomplete="new-password"
      required
      disabled={submitting}
    />
    <button class="btn primary" type="submit" disabled={submitting}>
      {submitting ? t.common.submitting : t.setup.submit}
    </button>
  </form>
</main>
