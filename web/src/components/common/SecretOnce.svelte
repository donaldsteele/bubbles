<script lang="ts">
  // Shows a secret (token, invite link, reset link, admin password) exactly once.
  // The caller keeps the value only in memory; this component stops showing it
  // and tells the caller to drop theirs when the admin confirms, when the
  // component goes away, or when the route changes.
  import { onMount, type Snippet } from 'svelte';
  import { t } from '../../lib/i18n/en';
  import { subscribe } from '../../lib/router';

  interface Props {
    secret: string;
    title: string;
    /** Called once when the secret is gone; the caller must drop its copy. */
    ondismiss: () => void;
    /** Extra content shown with the secret, e.g. a QR code of it. */
    children?: Snippet;
  }

  let { secret, title, ondismiss, children }: Props = $props();

  let gone = $state(false);
  let copied = $state(false);

  function dismiss() {
    if (gone) return;
    gone = true;
    ondismiss();
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(secret);
      copied = true;
    } catch {
      copied = false;
    }
  }

  onMount(() => {
    // Any later route change ends the display.
    const off = subscribe(dismiss);
    return () => {
      off();
      dismiss();
    };
  });
</script>

{#if !gone}
  <section class="secret" aria-labelledby="secret-title">
    <h3 id="secret-title">{title}</h3>
    <p class="warn">{t.admin.secretOnce}</p>
    <input
      class="value"
      readonly
      value={secret}
      aria-label={title}
      onfocus={(e) => e.currentTarget.select()}
    />
    {#if children}{@render children()}{/if}
    <div class="row">
      <button class="btn" type="button" onclick={copy}>{t.admin.copy}</button>
      <button class="btn primary" type="button" onclick={dismiss}>{t.admin.secretDone}</button>
      {#if copied}<span role="status">{t.admin.copied}</span>{/if}
    </div>
  </section>
{/if}

<style>
  .secret {
    border: 2px solid var(--accent);
    border-radius: var(--radius);
    padding: var(--s3);
    margin: var(--s3) 0;
    display: flex;
    flex-direction: column;
    gap: var(--s2);
    background: var(--bg-elev);
  }
  h3 {
    margin: 0;
    font-size: 16px;
  }
  .warn {
    margin: 0;
    font-weight: 600;
  }
  .value {
    width: 100%;
    font-family: var(--font-mono, monospace);
    padding: var(--s2);
    min-height: 40px;
    color: var(--fg);
    background: var(--bg);
    border: 1px solid var(--line);
    border-radius: var(--radius);
  }
  .row {
    display: flex;
    gap: var(--s2);
    align-items: center;
    flex-wrap: wrap;
  }
</style>
