<script lang="ts">
  import { onMount } from 'svelte';
  import { ApiError, api, call } from '../lib/api/client';
  import { completeSignIn, refreshStatus } from '../lib/boot';
  import { signupFailure, validatePassword, validateRequired } from '../lib/forms';
  import { fmt, t } from '../lib/i18n/en';
  import { session } from '../lib/state/session.svelte';
  import FormField from '../components/forms/FormField.svelte';

  let name = $state('');
  let password = $state('');
  let errors = $state<Record<string, string>>({});
  let formError = $state('');
  let submitting = $state(false);

  // The form follows the server's current state, not the one at page load.
  onMount(() => void refreshStatus().catch(() => {}));

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    errors = { name: validateRequired(name), password: validatePassword(password) };
    formError = '';
    if (errors.name || errors.password) return;
    submitting = true;
    try {
      const res = await call(api.POST('/register', { body: { name: name.trim(), password } }));
      await completeSignIn(res.user);
    } catch (err) {
      if (err instanceof ApiError && err.status === 403) session.registrationOpen = false;
      const f = signupFailure(err, { 403: t.register.closedBody });
      errors = f.fields;
      formError = f.form;
    } finally {
      submitting = false;
    }
  }
</script>

<main class="card">
  {#if !session.registrationOpen}
    <h1>{t.register.closed}</h1>
    <p>{t.register.closedBody}</p>
    <p><a href="/login">{t.register.signInLink}</a></p>
  {:else}
    <h1>
      {session.instanceName
        ? fmt(t.register.title, { instance: session.instanceName })
        : t.register.fallbackTitle}
    </h1>
    <form onsubmit={submit} novalidate>
      {#if formError}<p class="form-error" role="alert">{formError}</p>{/if}
      <FormField
        id="reg-name"
        label={t.fields.yourName}
        bind:value={name}
        hint={t.hints.yourName}
        error={errors.name}
        autocomplete="name"
        required
        disabled={submitting}
      />
      <FormField
        id="reg-password"
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
        {submitting ? t.common.submitting : t.register.submit}
      </button>
    </form>
    <p>{t.register.haveAccount} <a href="/login">{t.register.signInLink}</a></p>
  {/if}
</main>
