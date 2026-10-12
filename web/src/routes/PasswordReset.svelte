<script lang="ts">
  import { api, call } from '../lib/api/client';
  import { failureFrom, validatePassword } from '../lib/forms';
  import { t } from '../lib/i18n/en';
  import FormField from '../components/forms/FormField.svelte';

  let { code }: { code: string } = $props();

  let password = $state('');
  let errors = $state<Record<string, string>>({});
  let formError = $state('');
  let submitting = $state(false);
  let done = $state(false);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    errors = { password: validatePassword(password) };
    formError = '';
    if (errors.password) return;
    submitting = true;
    try {
      await call(
        api.POST('/password-resets/{code}', {
          params: { path: { code } },
          body: { password },
        }),
      );
      done = true;
    } catch (err) {
      const f = failureFrom(err, { 404: t.reset.notFound });
      errors = f.fields;
      formError = f.form;
    } finally {
      submitting = false;
    }
  }
</script>

<main class="card">
  <h1>{t.reset.title}</h1>
  {#if done}
    <p role="status">{t.reset.done}</p>
    <p><a href="/login">{t.reset.signIn}</a></p>
  {:else}
    <form onsubmit={submit} novalidate>
      {#if formError}<p class="form-error" role="alert">{formError}</p>{/if}
      <FormField
        id="reset-password"
        label={t.fields.newPassword}
        type="password"
        bind:value={password}
        hint={t.hints.password}
        error={errors.password}
        autocomplete="new-password"
        required
        disabled={submitting}
      />
      <button class="btn primary" type="submit" disabled={submitting}>
        {submitting ? t.common.submitting : t.reset.submit}
      </button>
    </form>
  {/if}
</main>
