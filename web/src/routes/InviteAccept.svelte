<script lang="ts">
  import { api, call } from '../lib/api/client';
  import { completeSignIn } from '../lib/boot';
  import { signupFailure, validatePassword, validateRequired } from '../lib/forms';
  import { fmt, t } from '../lib/i18n/en';
  import FormField from '../components/forms/FormField.svelte';

  let { code }: { code: string } = $props();

  let status = $state<'checking' | 'valid' | 'invalid'>('checking');
  let instanceName = $state('');
  let name = $state('');
  let password = $state('');
  let errors = $state<Record<string, string>>({});
  let formError = $state('');
  let submitting = $state(false);

  $effect(() => {
    const c = code;
    status = 'checking';
    call(api.GET('/invites/{code}', { params: { path: { code: c } } }))
      .then((info) => {
        instanceName = info.instance_name;
        status = info.valid ? 'valid' : 'invalid';
      })
      .catch(() => {
        status = 'invalid';
      });
  });

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    errors = { name: validateRequired(name), password: validatePassword(password) };
    formError = '';
    if (errors.name || errors.password) return;
    submitting = true;
    try {
      const res = await call(
        api.POST('/invites/{code}/accept', {
          params: { path: { code } },
          body: { name: name.trim(), password },
        }),
      );
      await completeSignIn(res.user);
    } catch (err) {
      const f = signupFailure(err, { 404: t.invite.invalid });
      errors = f.fields;
      formError = f.form;
    } finally {
      submitting = false;
    }
  }
</script>

<main class="card">
  {#if status === 'checking'}
    <h1>{t.invite.checking}</h1>
  {:else if status === 'invalid'}
    <h1>{t.invite.invalid}</h1>
    <p><a href="/login">{t.login.title}</a></p>
  {:else}
    <h1>{fmt(t.invite.title, { instance: instanceName })}</h1>
    <form onsubmit={submit} novalidate>
      {#if formError}<p class="form-error" role="alert">{formError}</p>{/if}
      <FormField
        id="invite-name"
        label={t.fields.yourName}
        bind:value={name}
        hint={t.hints.yourName}
        error={errors.name}
        autocomplete="name"
        required
        disabled={submitting}
      />
      <FormField
        id="invite-password"
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
        {submitting ? t.common.submitting : t.invite.submit}
      </button>
    </form>
  {/if}
</main>
