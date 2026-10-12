<script lang="ts">
  import { failureFrom } from '../../lib/forms';
  import { createChannel } from '../../lib/channel-actions';
  import { t } from '../../lib/i18n/en';
  import { navigate } from '../../lib/router';
  import Dialog from '../a11y/Dialog.svelte';
  import FormField from '../forms/FormField.svelte';

  let { onclose }: { onclose: () => void } = $props();

  let name = $state('');
  let topic = $state('');
  let kind = $state<'public' | 'private'>('public');
  let nameError = $state('');
  let formError = $state('');
  let busy = $state(false);

  /** Server rule: letters, digits and hyphens, 1-64 chars after trimming and dropping a leading #. */
  export function normalizeName(v: string): string {
    return v.trim().replace(/^#/, '').toLowerCase();
  }

  function validate(v: string): string {
    if (v === '') return t.common.required;
    return /^[a-z0-9-]{1,64}$/.test(v) ? '' : t.chat.nameInvalid;
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    const n = normalizeName(name);
    nameError = validate(n);
    formError = '';
    if (nameError) return;
    busy = true;
    try {
      const ch = await createChannel(n, kind, topic.trim());
      onclose();
      navigate(`/c/${ch.id}`);
    } catch (err) {
      const f = failureFrom(err, { 409: t.chat.nameInvalid });
      nameError = f.fields['name'] ?? '';
      formError = f.form;
    } finally {
      busy = false;
    }
  }
</script>

<Dialog title={t.chat.create} {onclose}>
  <form onsubmit={submit} novalidate>
    {#if formError}<p class="form-error" role="alert">{formError}</p>{/if}
    <FormField
      id="cc-name"
      label={t.chat.name}
      bind:value={name}
      error={nameError}
      hint={t.chat.nameHint}
      required
    />
    <FormField id="cc-topic" label={t.chat.topic} bind:value={topic} />
    <fieldset>
      <legend>{t.chat.visibility}</legend>
      <label><input type="radio" bind:group={kind} value="public" /> {t.chat.public}</label>
      <label><input type="radio" bind:group={kind} value="private" /> {t.chat.private}</label>
    </fieldset>
    <button class="btn primary" type="submit" disabled={busy}>{t.chat.createSubmit}</button>
  </form>
</Dialog>

<style>
  fieldset {
    border: 0;
    padding: 0;
    margin: 0 0 var(--s4);
  }
  label {
    display: flex;
    align-items: center;
    gap: var(--s2);
    min-height: 32px;
  }
</style>
