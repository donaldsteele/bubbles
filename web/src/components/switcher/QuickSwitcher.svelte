<script lang="ts">
  import { listboxKey } from '../../lib/composer/autocomplete';
  import { channelLabel } from '../../lib/channels';
  import { t } from '../../lib/i18n/en';
  import { navigate } from '../../lib/router';
  import { rankEntries, type SwitcherEntry } from '../../lib/switcher';
  import { channels } from '../../lib/state/channels.svelte';
  import { session } from '../../lib/state/session.svelte';
  import { users } from '../../lib/state/users.svelte';
  import Dialog from '../a11y/Dialog.svelte';

  let { onclose }: { onclose: () => void } = $props();

  let query = $state('');
  let active = $state(0);

  const entries = $derived<SwitcherEntry[]>(
    channels.joined.map((c) => ({
      id: c.id,
      label: channelLabel(c, users.byId, session.me?.id ?? ''),
      dm: c.kind === 'dm' || c.kind === 'group_dm',
    })),
  );
  const matches = $derived(rankEntries(query, entries));

  $effect(() => {
    void query;
    active = 0;
  });

  function go(i: number) {
    const m = matches[i];
    if (!m) return;
    navigate(`/c/${m.id}`);
    onclose();
  }

  function onkeydown(e: KeyboardEvent) {
    const r = listboxKey(e.key, active, matches.length);
    if (r.action === 'move') {
      e.preventDefault();
      active = r.active;
    } else if (r.action === 'accept' && e.key === 'Enter') {
      e.preventDefault();
      go(r.active);
    }
  }
</script>

<Dialog title={t.rich.switcher} {onclose}>
  <label class="visually-hidden" for="switcher-q">{t.rich.switcherPlaceholder}</label>
  <input
    id="switcher-q"
    type="text"
    role="combobox"
    aria-expanded="true"
    aria-controls="switcher-list"
    aria-autocomplete="list"
    aria-activedescendant={matches.length > 0 ? `switcher-opt-${active}` : undefined}
    autocomplete="off"
    placeholder={t.rich.switcherPlaceholder}
    data-autofocus
    bind:value={query}
    {onkeydown}
  />
  <ul id="switcher-list" role="listbox" aria-label={t.rich.switcher}>
    {#each matches as m, i (m.id)}
      <!-- svelte-ignore a11y_click_events_have_key_events -->
      <li
        id={`switcher-opt-${i}`}
        role="option"
        aria-selected={i === active}
        class:active={i === active}
        onclick={() => go(i)}
      >
        {m.dm ? '' : '#'}{m.label}
      </li>
    {/each}
  </ul>
  {#if matches.length === 0}<p class="none" role="status">{t.rich.switcherNone}</p>{/if}
</Dialog>

<style>
  input {
    width: 100%;
    min-height: 40px;
    margin: var(--s3) 0 var(--s2);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    padding: var(--s2) var(--s3);
    min-height: 36px;
    cursor: pointer;
    border-radius: var(--radius);
  }
  li.active {
    background: var(--accent);
    color: var(--accent-fg);
  }
  .none {
    color: var(--fg-muted);
  }
</style>
