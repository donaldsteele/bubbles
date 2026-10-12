<script lang="ts">
  import { failureFrom } from '../../lib/forms';
  import { startDm } from '../../lib/channel-actions';
  import { t } from '../../lib/i18n/en';
  import { navigate } from '../../lib/router';
  import { session } from '../../lib/state/session.svelte';
  import { users } from '../../lib/state/users.svelte';
  import Dialog from '../a11y/Dialog.svelte';

  let { onclose }: { onclose: () => void } = $props();

  const MAX_OTHERS = 8;

  let query = $state('');
  let selected = $state<string[]>([]);
  let error = $state('');
  let busy = $state(false);

  const candidates = $derived(
    Object.values(users.byId)
      .filter((u) => u.id !== session.me?.id && !u.deactivated && u.kind === 'human')
      .filter((u) => {
        const q = query.trim().toLowerCase();
        return (
          q === '' ||
          u.username.toLowerCase().includes(q) ||
          u.display_name.toLowerCase().includes(q)
        );
      })
      .sort((a, b) => (a.display_name || a.username).localeCompare(b.display_name || b.username)),
  );

  function toggle(id: string, on: boolean) {
    selected = on ? [...selected, id] : selected.filter((x) => x !== id);
    error = selected.length > MAX_OTHERS ? t.chat.tooMany : '';
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (selected.length > MAX_OTHERS) {
      error = t.chat.tooMany;
      return;
    }
    busy = true;
    try {
      // No one selected opens the note-to-self conversation.
      const ch = await startDm(selected);
      onclose();
      navigate(`/c/${ch.id}`);
    } catch (err) {
      error = failureFrom(err).form;
    } finally {
      busy = false;
    }
  }
</script>

<Dialog title={t.chat.newDm} {onclose}>
  <form onsubmit={submit}>
    <div class="field">
      <label for="dm-filter">{t.chat.filter}</label>
      <input id="dm-filter" type="search" bind:value={query} autocomplete="off" />
      <span class="hint">{t.chat.peopleHint}</span>
    </div>
    <fieldset>
      <legend>{t.chat.people}</legend>
      <ul>
        {#each candidates as u (u.id)}
          <li>
            <label>
              <input
                type="checkbox"
                checked={selected.includes(u.id)}
                onchange={(e) => toggle(u.id, e.currentTarget.checked)}
              />
              {u.display_name || u.username}
              <span class="muted">@{u.username}</span>
            </label>
          </li>
        {/each}
      </ul>
    </fieldset>
    {#if error}<p class="form-error" role="alert">{error}</p>{/if}
    <button class="btn primary" type="submit" disabled={busy || selected.length > MAX_OTHERS}>
      {t.chat.startSubmit}
    </button>
  </form>
</Dialog>

<style>
  fieldset {
    border: 0;
    padding: 0;
    margin: 0 0 var(--s4);
    max-height: 40vh;
    overflow-y: auto;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  label {
    display: flex;
    align-items: center;
    gap: var(--s2);
    min-height: 36px;
  }
  .muted {
    color: var(--fg-muted);
    font-size: 13px;
  }
</style>
