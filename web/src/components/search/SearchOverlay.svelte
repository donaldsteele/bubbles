<script lang="ts">
  import { api, call } from '../../lib/api/client';
  import type { Schemas } from '../../lib/api/client';
  import { fmt, t } from '../../lib/i18n/en';
  import { clockTime } from '../../lib/messages/time';
  import { navigate } from '../../lib/router';
  import {
    buildQuery,
    emptyFilters,
    sanitizeSnippet,
    type SearchFilters,
  } from '../../lib/search/snippet';
  import { channels } from '../../lib/state/channels.svelte';
  import { ui } from '../../lib/state/ui.svelte';
  import { users } from '../../lib/state/users.svelte';
  import Dialog from '../a11y/Dialog.svelte';

  let { onclose }: { onclose: () => void } = $props();

  type Result = Schemas['SearchResult'];

  let text = $state('');
  let filters = $state<SearchFilters>({ ...emptyFilters });
  let results = $state<Result[]>([]);
  let next = $state<string | null>(null);
  let phase = $state<'idle' | 'loading' | 'done' | 'failed'>('idle');

  const roomNames = $derived(
    channels.joined
      .filter((c) => c.name && (c.kind === 'public' || c.kind === 'private'))
      .map((c) => c.name as string)
      .sort(),
  );
  const people = $derived(
    Object.values(users.byId)
      .filter((u) => !u.deactivated)
      .sort((a, b) => a.username.localeCompare(b.username)),
  );

  async function run(more: boolean) {
    const q = buildQuery(text, filters);
    if (q === '') return;
    phase = 'loading';
    try {
      const page = await call(
        api.GET('/search', {
          params: { query: { q, limit: 25, ...(more && next ? { cursor: next } : {}) } },
        }),
      );
      results = more ? [...results, ...page.items] : page.items;
      next = page.next_cursor;
      phase = 'done';
    } catch {
      phase = 'failed';
    }
  }

  function chName(id: string): string {
    const c = channels.byId[id];
    return c?.name ? `#${c.name}` : '';
  }

  function open(r: Result) {
    const m = r.message;
    ui.jump = { channelId: m.channel_id, messageId: m.id };
    navigate(m.thread_root_id ? `/c/${m.channel_id}/t/${m.thread_root_id}` : `/c/${m.channel_id}`);
    onclose();
  }
</script>

<Dialog title={t.rich.search} {onclose}>
  <form
    class="search"
    role="search"
    onsubmit={(e) => {
      e.preventDefault();
      void run(false);
    }}
  >
    <label class="visually-hidden" for="search-q">{t.rich.searchPlaceholder}</label>
    <input
      id="search-q"
      type="search"
      maxlength="256"
      autocomplete="off"
      placeholder={t.rich.searchPlaceholder}
      data-autofocus
      bind:value={text}
    />
    <div class="chips" role="group" aria-label={t.rich.searchFilters}>
      <label>
        <span>{t.rich.filterIn}</span>
        <select bind:value={filters.channel}>
          <option value="">{t.rich.anyChannel}</option>
          {#each roomNames as n (n)}<option value={n}>#{n}</option>{/each}
        </select>
      </label>
      <label>
        <span>{t.rich.filterFrom}</span>
        <select bind:value={filters.from}>
          <option value="">{t.rich.anyone}</option>
          {#each people as u (u.id)}<option value={u.username}>@{u.username}</option>{/each}
        </select>
      </label>
      <label>
        <span>{t.rich.filterHas}</span>
        <select bind:value={filters.has}>
          <option value="">{t.rich.hasAny}</option>
          <option value="file">{t.rich.hasFile}</option>
          <option value="image">{t.rich.hasImage}</option>
          <option value="link">{t.rich.hasLink}</option>
        </select>
      </label>
      <label>
        <span>{t.rich.filterBefore}</span>
        <input type="date" bind:value={filters.before} />
      </label>
      <label>
        <span>{t.rich.filterAfter}</span>
        <input type="date" bind:value={filters.after} />
      </label>
    </div>
    <button class="btn primary" type="submit">{t.rich.search}</button>
  </form>

  <div aria-live="polite">
    {#if phase === 'loading' && results.length === 0}
      <p class="status">{t.rich.searching}</p>
    {:else if phase === 'failed'}
      <p class="form-error" role="alert">{t.rich.searchFailed}</p>
    {:else if phase === 'done' && results.length === 0}
      <p class="status">{t.rich.noResults}</p>
    {/if}
  </div>

  <ul class="results" role="list">
    {#each results as r (r.message.id)}
      <li>
        <button class="hit" type="button" onclick={() => open(r)}>
          <span class="meta">
            <strong>{r.message.author.display_name || r.message.author.username}</strong>
            <span>{chName(r.message.channel_id)}</span>
            <time datetime={r.message.created_at}>{clockTime(r.message.created_at)}</time>
          </span>
          <!-- eslint-disable-next-line svelte/no-at-html-tags -- sanitized to <mark> only -->
          <span class="snippet">{@html sanitizeSnippet(r.snippet)}</span>
        </button>
      </li>
    {/each}
  </ul>
  {#if next && phase !== 'loading'}
    <button class="btn" type="button" onclick={() => run(true)}>{t.rich.loadMore}</button>
  {/if}
  {#if phase === 'done' && results.length > 0}
    <p class="status">{fmt(t.rich.results, { n: String(results.length) })}</p>
  {/if}
</Dialog>

<style>
  .search {
    display: flex;
    flex-direction: column;
    gap: var(--s2);
    margin: var(--s3) 0;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: var(--s2);
  }
  .chips label {
    display: flex;
    flex-direction: column;
    font-size: 12px;
    color: var(--fg-muted);
  }
  .chips select,
  .chips input {
    min-height: 32px;
    background: var(--bg-elev2);
    border: 1px solid var(--line-strong);
    border-radius: var(--radius);
  }
  .results {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .hit {
    display: block;
    width: 100%;
    text-align: left;
    padding: var(--s2);
    background: none;
    border: 0;
    border-bottom: 1px solid var(--line);
    cursor: pointer;
    min-height: 44px;
  }
  .hit:hover {
    background: var(--bg-elev2);
  }
  .meta {
    display: flex;
    gap: var(--s2);
    align-items: baseline;
    font-size: 12px;
    color: var(--fg-muted);
  }
  .meta strong {
    color: var(--fg);
    font-size: 14px;
  }
  .snippet {
    display: block;
    overflow-wrap: anywhere;
  }
  .snippet :global(mark) {
    background: var(--warnbg);
    color: inherit;
    outline: 1px solid var(--warn);
  }
  .status {
    color: var(--fg-muted);
  }
</style>
