<script lang="ts">
  // Stand-in content for routes whose screens land in later milestones.
  let { title, body = '' }: { title: string; body?: string } = $props();
</script>

<h1>{title}</h1>
{#if body}<p>{body}</p>{/if}
