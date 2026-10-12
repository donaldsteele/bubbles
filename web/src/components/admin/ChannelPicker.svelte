<script lang="ts">
  import { toggle } from '../../lib/admin/scopes';
  import type { Channel } from '../../lib/api/types';
  import { t } from '../../lib/i18n/en';

  let {
    channels,
    selected = $bindable(),
    legend = t.admin.channelAllowlist,
    help = t.admin.channelAllowlistHelp,
    disabled = false,
  }: {
    channels: Channel[];
    selected: string[];
    legend?: string;
    help?: string;
    disabled?: boolean;
  } = $props();
</script>

<fieldset>
  <legend>{legend}</legend>
  <p class="hint">{help}</p>
  {#each channels.filter((c) => c.kind === 'public' || c.kind === 'private') as c (c.id)}
    <label class="inline">
      <input
        type="checkbox"
        checked={selected.includes(c.id)}
        {disabled}
        onchange={(e) => (selected = toggle(selected, c.id, e.currentTarget.checked))}
      />
      #{c.name}
      {#if c.kind === 'private'}<span class="chip">{t.admin.private}</span>{/if}
    </label>
  {/each}
</fieldset>
