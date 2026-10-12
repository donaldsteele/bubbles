<script lang="ts">
  import { onMount } from 'svelte';
  import { api, call } from '../lib/api/client';
  import { completeSignIn, refreshStatus } from '../lib/boot';
  import { failureFrom, validateRequired } from '../lib/forms';
  import { t } from '../lib/i18n/en';
  import FormField from '../components/forms/FormField.svelte';
  import { session } from '../lib/state/session.svelte';

  let username = $state('');
  let password = $state('');
  let errors = $state<Record<string, string>>({});
  let formError = $state('');
  let submitting = $state(false);

  // The sign-up link follows the server's current state, not the one at page load.
  onMount(() => void refreshStatus().catch(() => {}));

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    errors = { username: validateRequired(username), password: validateRequired(password) };
    formError = '';
    if (errors.username || errors.password) return;
    submitting = true;
    try {
      const res = await call(
        api.POST('/auth/login', { body: { username: username.trim(), password } }),
      );
      await completeSignIn(res.user, new URLSearchParams(location.search).get('next'));
    } catch (err) {
      const f = failureFrom(err, { 401: t.login.invalid });
      errors = f.fields;
      formError = f.form;
    } finally {
      submitting = false;
    }
  }
</script>

<main class="card">
  <h1>{t.login.title}</h1>
  <form onsubmit={submit} novalidate>
    {#if formError}<p class="form-error" role="alert">{formError}</p>{/if}
    <FormField
      id="login-username"
      label={t.fields.nameOrUsername}
      bind:value={username}
      error={errors.username}
      autocomplete="username"
      required
      disabled={submitting}
    />
    <FormField
      id="login-password"
      label={t.fields.password}
      type="password"
      bind:value={password}
      error={errors.password}
      autocomplete="current-password"
      required
      disabled={submitting}
    />
    <button class="btn primary" type="submit" disabled={submitting}>
      {submitting ? t.common.submitting : t.login.submit}
    </button>
  </form>
  {#if session.registrationOpen}
    <p>{t.login.newHere} <a href="/register">{t.login.createAccountLink}</a></p>
  {:else}
    <p class="hint">{t.login.askForInvite}</p>
  {/if}
</main>
