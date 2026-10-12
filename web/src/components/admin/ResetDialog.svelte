<script lang="ts">
  import { Trash2 } from '@lucide/svelte';
  import Dialog from '../a11y/Dialog.svelte';
  import FormField from '../forms/FormField.svelte';
  import { downloadBackup } from '../../lib/admin/backup';
  import { resetErrorMessage, resetFlow } from '../../lib/admin/resetFlow.svelte';
  import { canConfirmReset } from '../../lib/admin/validate';
  import { t } from '../../lib/i18n/en';

  interface Props {
    mode: 'content' | 'factory';
    /** The instance name the admin must type. */
    instanceName: string;
    onclose: () => void;
    /** A content clear finished (a factory reset ends in the result screen). */
    oncleared: (epoch: number) => void;
  }

  let { mode, instanceName, onclose, oncleared }: Props = $props();

  let backup = $state(false);
  let passphrase = $state('');
  let rotateCa = $state(false);
  let keepSettings = $state(false);
  let typed = $state('');
  let password = $state('');
  let busy = $state(false);
  let error = $state('');
  let step = $state('');

  const factory = $derived(mode === 'factory');
  const ready = $derived(
    canConfirmReset(instanceName, typed, password) && (!backup || passphrase.length >= 1),
  );

  async function confirm(e: SubmitEvent) {
    e.preventDefault();
    if (!ready || busy) return;
    busy = true;
    error = '';
    try {
      if (backup) {
        step = t.danger.stepBackup;
        await downloadBackup(passphrase);
      }
      step = t.danger.stepDeleting;
      const epoch = await resetFlow.run({
        mode,
        confirm: typed.trim(),
        password,
        rotateCa: factory && rotateCa,
        keepSettings: factory && keepSettings,
      });
      password = '';
      if (epoch !== null) oncleared(epoch);
      onclose();
    } catch (err) {
      error = resetErrorMessage(err);
    } finally {
      busy = false;
      step = '';
      passphrase = '';
    }
  }
</script>

<Dialog alert title={factory ? t.danger.factoryDialogTitle : t.danger.contentDialogTitle} {onclose}>
  <form class="reset-form" onsubmit={confirm} novalidate>
    <p class="warn">{factory ? t.danger.factoryDialogBody : t.danger.contentDialogBody}</p>

    <label class="inline">
      <input type="checkbox" bind:checked={backup} disabled={busy} />
      {t.danger.backupFirst}
    </label>
    {#if backup}
      <FormField
        id="reset-passphrase"
        label={t.danger.backupPassphrase}
        type="password"
        bind:value={passphrase}
        hint={t.danger.backupPassphraseHint}
        autocomplete="new-password"
        required
        disabled={busy}
      />
    {/if}

    {#if factory}
      <label class="inline">
        <input type="checkbox" bind:checked={rotateCa} disabled={busy} />
        {t.danger.rotateCa}
      </label>
      <label class="inline">
        <input type="checkbox" bind:checked={keepSettings} disabled={busy} />
        {t.danger.keepSettings}
      </label>
    {/if}

    <FormField
      id="reset-confirm"
      label={t.danger.typeInstanceName}
      bind:value={typed}
      hint={`${t.danger.typeInstanceNameHint} ${instanceName}`}
      autocomplete="off"
      required
      disabled={busy}
    />
    <FormField
      id="reset-password"
      label={t.danger.yourPassword}
      type="password"
      bind:value={password}
      autocomplete="current-password"
      required
      disabled={busy}
    />

    {#if error}<p class="form-error" role="alert">{error}</p>{/if}
    {#if step}<p role="status">{step}</p>{/if}

    <div class="actions">
      <button class="btn" type="button" data-autofocus onclick={onclose} disabled={busy}
        >{t.common.cancel}</button
      >
      <button class="btn danger" type="submit" disabled={!ready || busy}>
        <Trash2 aria-hidden="true" size={16} />
        {factory ? t.danger.deleteEverything : t.danger.deleteContent}
      </button>
    </div>
  </form>
</Dialog>

<style>
  .reset-form {
    display: flex;
    flex-direction: column;
    gap: var(--s2);
  }
  .warn {
    margin: 0;
    padding: var(--s2) var(--s3);
    border: 1px solid var(--bad);
    background: var(--badbg);
    border-radius: var(--radius);
    font-weight: 600;
  }
  .inline {
    display: flex;
    align-items: center;
    gap: var(--s2);
    min-height: 32px;
  }
  .actions {
    display: flex;
    gap: var(--s2);
    justify-content: flex-end;
    flex-wrap: wrap;
  }
  .danger {
    display: inline-flex;
    align-items: center;
    gap: var(--s2);
    background: var(--bad);
    color: var(--bg);
    border-color: var(--bad);
    font-weight: 700;
  }
</style>
