<script lang="ts">
  import { onMount, tick } from 'svelte';
  import {
    applyTone,
    loadEmoji,
    loadRecents,
    loadTone,
    pushRecent,
    saveTone,
    searchEmoji,
    type EmojiData,
  } from '../../lib/emoji/data';
  import { t } from '../../lib/i18n/en';
  import Dialog from '../a11y/Dialog.svelte';

  interface Props {
    onpick: (emoji: string) => void;
    onclose: () => void;
  }

  let { onpick, onclose }: Props = $props();

  const COLUMNS = 8;
  const GROUP_LABELS: Record<string, string> = {
    'smileys-emotion': 'Smileys and emotion',
    'people-body': 'People and body',
    'animals-nature': 'Animals and nature',
    'food-drink': 'Food and drink',
    'travel-places': 'Travel and places',
    activities: 'Activities',
    objects: 'Objects',
    symbols: 'Symbols',
    flags: 'Flags',
  };

  interface Cell {
    char: string;
    label: string;
  }

  let data = $state<EmojiData | null>(null);
  let failed = $state(false);
  let query = $state('');
  let tab = $state<number | 'recent'>(loadRecents().length > 0 ? 'recent' : 0);
  let tone = $state(loadTone());
  let recents = $state(loadRecents());
  let active = $state(0);
  let grid: HTMLDivElement | undefined = $state();

  onMount(() => {
    loadEmoji()
      .then((d) => {
        data = d;
      })
      .catch(() => {
        failed = true;
      });
  });

  const tabs = $derived.by(() => {
    if (!data) return [];
    const out: { id: number | 'recent'; label: string; icon: string }[] = [];
    if (recents.length > 0) out.push({ id: 'recent', label: t.rich.emojiRecent, icon: '🕘' });
    data.groups.forEach((name, i) => {
      const first = data?.items.find((it) => it.g === i);
      if (first) out.push({ id: i, label: GROUP_LABELS[name] ?? name, icon: first.e });
    });
    return out;
  });

  const cells = $derived.by((): Cell[] => {
    if (!data) return [];
    if (query.trim() !== '') {
      return searchEmoji(data, query, 96).map((it) => ({
        char: applyTone(it, tone),
        label: it.n,
      }));
    }
    if (tab === 'recent') {
      const names = new Map(data.items.map((it) => [it.e, it.n]));
      return recents.map((e) => ({ char: e, label: names.get(e) ?? e }));
    }
    return data.items
      .filter((it) => it.g === tab)
      .map((it) => ({ char: applyTone(it, tone), label: it.n }));
  });

  $effect(() => {
    void cells;
    active = 0;
  });

  function pick(c: Cell) {
    recents = pushRecent(c.char);
    onpick(c.char);
  }

  function setTone(v: number) {
    tone = v;
    saveTone(v);
  }

  async function move(to: number) {
    const n = cells.length;
    if (n === 0) return;
    active = Math.max(0, Math.min(n - 1, to));
    await tick();
    grid?.querySelector<HTMLElement>(`[data-idx="${active}"]`)?.focus();
  }

  function onkeydown(e: KeyboardEvent) {
    const step: Record<string, number> = {
      ArrowRight: 1,
      ArrowLeft: -1,
      ArrowDown: COLUMNS,
      ArrowUp: -COLUMNS,
    };
    const d = step[e.key];
    if (d !== undefined) {
      e.preventDefault();
      void move(active + d);
    } else if (e.key === 'Home') {
      e.preventDefault();
      void move(0);
    } else if (e.key === 'End') {
      e.preventDefault();
      void move(cells.length - 1);
    }
  }

  const toneNames = [
    t.rich.toneDefault,
    t.rich.tone1,
    t.rich.tone2,
    t.rich.tone3,
    t.rich.tone4,
    t.rich.tone5,
  ];
</script>

<Dialog title={t.rich.emojiPicker} {onclose}>
  <div class="bar">
    <label class="visually-hidden" for="emoji-search">{t.rich.emojiSearch}</label>
    <input
      id="emoji-search"
      type="search"
      placeholder={t.rich.emojiSearch}
      autocomplete="off"
      data-autofocus
      bind:value={query}
    />
    <label class="tone">
      <span class="visually-hidden">{t.rich.skinTone}</span>
      <select value={tone} onchange={(e) => setTone(Number(e.currentTarget.value))}>
        {#each toneNames as name, i (i)}
          <option value={i}>{name}</option>
        {/each}
      </select>
    </label>
  </div>

  {#if failed}
    <p class="form-error" role="alert">{t.rich.emojiFailed}</p>
  {:else if !data}
    <p role="status">{t.rich.emojiLoading}</p>
  {:else}
    {#if query.trim() === ''}
      <div class="tabs" role="group" aria-label={t.rich.emoji}>
        {#each tabs as tb (tb.id)}
          <button
            class="tab"
            type="button"
            aria-label={tb.label}
            title={tb.label}
            aria-pressed={tab === tb.id}
            onclick={() => (tab = tb.id)}
          >
            <span aria-hidden="true">{tb.icon}</span>
          </button>
        {/each}
      </div>
    {/if}
    {#if cells.length === 0}
      <p role="status">{t.rich.emojiNone}</p>
    {:else}
      <div
        class="grid"
        role="toolbar"
        tabindex="-1"
        aria-label={t.rich.emoji}
        bind:this={grid}
        {onkeydown}
      >
        {#each cells as c, i (c.char + i)}
          <button
            class="cell"
            type="button"
            tabindex={i === active ? 0 : -1}
            data-idx={i}
            aria-label={c.label}
            title={c.label}
            onclick={() => pick(c)}
          >
            <span aria-hidden="true">{c.char}</span>
          </button>
        {/each}
      </div>
    {/if}
  {/if}
</Dialog>

<style>
  .bar {
    display: flex;
    gap: var(--s2);
    margin: var(--s3) 0;
  }
  input[type='search'] {
    flex: 1;
    min-height: 36px;
  }
  select {
    min-height: 36px;
    background: var(--bg-elev2);
    border: 1px solid var(--line-strong);
    border-radius: var(--radius);
  }
  .tabs {
    display: flex;
    flex-wrap: wrap;
    gap: var(--s1);
    margin-bottom: var(--s2);
  }
  .tab,
  .cell {
    min-width: 36px;
    min-height: 36px;
    display: grid;
    place-items: center;
    background: none;
    border: 1px solid transparent;
    border-radius: var(--radius);
    font-size: 22px;
    line-height: 1;
    cursor: pointer;
  }
  .tab[aria-pressed='true'] {
    border-color: var(--accent);
    background: var(--bg-elev2);
  }
  .tab:hover,
  .cell:hover {
    background: var(--bg-elev2);
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(8, 1fr);
    gap: 2px;
    max-height: 280px;
    overflow-y: auto;
  }
</style>
