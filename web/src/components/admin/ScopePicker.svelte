<script lang="ts">
  import { SCOPES, toggle, type Scope } from '../../lib/admin/scopes';
  import { t } from '../../lib/i18n/en';

  let {
    scopes = $bindable(),
    error = '',
    idPrefix = 'scope',
    disabled = false,
  }: { scopes: Scope[]; error?: string; idPrefix?: string; disabled?: boolean } = $props();
</script>

<fieldset aria-describedby={error ? `${idPrefix}-err` : undefined}>
  <legend>{t.admin.scopes}</legend>
  {#each SCOPES as s (s)}
    <label class="inline">
      <input
        type="checkbox"
        checked={scopes.includes(s)}
        {disabled}
        onchange={(e) => (scopes = toggle(scopes, s, e.currentTarget.checked))}
      />
      <span><code>{s}</code> <span class="hint">{t.admin.scopeHints[s]}</span></span>
    </label>
  {/each}
  {#if error}<p id={`${idPrefix}-err`} class="field-error" role="alert">{error}</p>{/if}
</fieldset>
