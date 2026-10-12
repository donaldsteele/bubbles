<script lang="ts">
  import { markdownEnv } from '../../lib/markdown/env.svelte';
  import { renderMarkdown } from '../../lib/markdown/render';

  let { source }: { source: string } = $props();

  // renderMarkdown sanitizes with DOMPurify; this is the only {@html} in the app.
  const html = $derived(renderMarkdown(source, markdownEnv));
</script>

<!-- eslint-disable-next-line svelte/no-at-html-tags -->
<div class="md">{@html html}</div>

<style>
  .md {
    overflow-wrap: anywhere;
  }
  .md :global(p) {
    margin: 0;
  }
  .md :global(p + p),
  .md :global(ul),
  .md :global(ol),
  .md :global(pre),
  .md :global(blockquote) {
    margin-top: var(--s2);
    margin-bottom: 0;
  }
  .md :global(pre) {
    background: var(--bg-elev2);
    border: 1px solid var(--line);
    border-radius: var(--radius);
    padding: var(--s2) var(--s3);
    overflow-x: auto;
  }
  .md :global(code) {
    background: var(--bg-elev2);
    border-radius: 3px;
    padding: 0 3px;
  }
  .md :global(pre code) {
    background: none;
    padding: 0;
  }
  .md :global(blockquote) {
    border-left: 3px solid var(--line-strong);
    margin-left: 0;
    padding-left: var(--s3);
    color: var(--fg-muted);
  }
  .md :global(.mention) {
    background: var(--infobg);
    border-radius: 3px;
    padding: 0 3px;
    font-weight: 600;
  }
  .md :global(a) {
    text-decoration: underline;
  }
</style>
