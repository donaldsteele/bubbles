<script lang="ts">
  import QrImage from '../common/QrImage.svelte';
  import SecretOnce from '../common/SecretOnce.svelte';
  import { groupFingerprint } from '../../lib/admin/format';
  import { resetFlow } from '../../lib/admin/resetFlow.svelte';
  import { t } from '../../lib/i18n/en';

  const result = $derived(resetFlow.factory);
  const setupHref = $derived(resetFlow.setupHref());

  /** The token is gone from memory once the admin has saved it. */
  function forgetToken() {
    if (resetFlow.factory) resetFlow.factory = { ...resetFlow.factory, setup_token: '' };
  }

  function start() {
    const href = resetFlow.setupHref();
    resetFlow.dismiss();
    location.assign(href);
  }
</script>

<div class="screen" role="dialog" aria-modal="true" aria-labelledby="reset-done-title">
  {#if resetFlow.phase === 'failed'}
    <h1 id="reset-done-title" tabindex="-1">{t.danger.failedTitle}</h1>
    <p class="form-error" role="alert">{resetFlow.error}</p>
    <p>{t.danger.failedHelp}</p>
    <button class="btn primary" type="button" onclick={() => resetFlow.dismiss()}
      >{t.danger.close}</button
    >
  {:else if result}
    <h1 id="reset-done-title" tabindex="-1">{t.danger.doneTitle}</h1>
    <p>{t.danger.doneBody}</p>

    <SecretOnce title={t.danger.setupToken} secret={result.setup_token} ondismiss={forgetToken}>
      <p class="hint">{t.danger.setupUrl}</p>
      <p class="mono"><a href={setupHref}>{setupHref}</a></p>
      <QrImage value={setupHref} label={t.danger.qrSetup} />
    </SecretOnce>

    {#if result.ca_rotated}
      <div class="note" role="note">
        <strong>{t.danger.newCa}</strong>
        <p>{t.danger.newCaHelp}</p>
        <p class="mono">{groupFingerprint(result.ca_fingerprint)}</p>
      </div>
    {/if}
    <button class="btn primary" type="button" onclick={start}>{t.danger.startSetup}</button>
  {:else}
    <h1 id="reset-done-title" tabindex="-1">{t.danger.working}</h1>
    <p role="status">{t.danger.workingHelp}</p>
  {/if}
</div>

<style>
  .screen {
    position: fixed;
    inset: 0;
    z-index: 200;
    overflow: auto;
    padding: var(--s5);
    background: var(--bg);
    color: var(--fg);
    display: flex;
    flex-direction: column;
    gap: var(--s3);
    align-items: center;
  }
  .screen > :global(*) {
    width: min(36rem, 100%);
  }
  h1 {
    font-size: 22px;
    margin: 0;
  }
  .mono {
    font-family: var(--font-mono);
    word-break: break-all;
  }
  .hint {
    color: var(--fg-muted);
    font-size: 13px;
    margin: 0;
  }
  .note {
    border: 1px solid var(--warn);
    background: var(--warnbg);
    border-radius: var(--radius);
    padding: var(--s3);
  }
  .note p {
    margin: var(--s1) 0 0;
  }
</style>
