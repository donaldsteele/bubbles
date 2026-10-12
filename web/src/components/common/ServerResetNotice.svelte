<script lang="ts">
  import { t } from '../../lib/i18n/en';
  import { resetNotice } from '../../lib/state/resetNotice.svelte';
</script>

{#if resetNotice.level}
  <div class="notice" role="alert">
    <p>
      <strong>{t.resetNotice.title}</strong>
      {resetNotice.level === 'factory' ? t.resetNotice.factory : t.resetNotice.content}
    </p>
    <button class="btn" type="button" onclick={() => resetNotice.dismiss()}
      >{t.resetNotice.dismiss}</button
    >
  </div>
{/if}

<style>
  .notice {
    display: flex;
    gap: var(--s3);
    align-items: center;
    justify-content: space-between;
    padding: var(--s2) var(--s4);
    background: var(--warnbg);
    color: var(--fg);
    border-bottom: 2px solid var(--warn);
  }
  p {
    margin: 0;
  }
</style>
