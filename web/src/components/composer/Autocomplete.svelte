<script lang="ts">
  import type { Suggestion } from '../../lib/composer/autocomplete';
  import { t } from '../../lib/i18n/en';

  interface Props {
    /** DOM id of the listbox (the textarea points at it with aria-controls). */
    id: string;
    items: Suggestion[];
    active: number;
    onpick: (index: number) => void;
  }

  let { id, items, active, onpick }: Props = $props();
</script>

<ul {id} class="list" role="listbox" aria-label={t.rich.suggestions}>
  {#each items as item, i (item.key)}
    <li
      id={`${id}-opt-${i}`}
      role="option"
      aria-selected={i === active}
      class:active={i === active}
      onmousedown={(e) => {
        e.preventDefault();
        onpick(i);
      }}
    >
      <span class="label">{item.label}</span>
      {#if item.detail}<span class="detail">{item.detail}</span>{/if}
    </li>
  {/each}
</ul>

<style>
  .list {
    position: absolute;
    left: var(--s4);
    right: var(--s4);
    bottom: 100%;
    z-index: 20;
    margin: 0;
    padding: var(--s1);
    list-style: none;
    max-height: 240px;
    overflow-y: auto;
    background: var(--bg-elev);
    border: 1px solid var(--line-strong);
    border-radius: var(--radius);
    box-shadow: 0 4px 16px var(--shadow-soft);
  }
  li {
    display: flex;
    gap: var(--s3);
    align-items: baseline;
    padding: var(--s1) var(--s2);
    min-height: 28px;
    cursor: pointer;
  }
  li.active {
    background: var(--accent);
    color: var(--accent-fg);
  }
  .detail {
    color: var(--fg-muted);
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  li.active .detail {
    color: inherit;
  }
</style>
