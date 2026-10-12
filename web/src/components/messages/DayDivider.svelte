<script lang="ts">
  import { dayLabel } from '../../lib/messages/time';

  let { ts }: { ts: number } = $props();
</script>

<div class="divider"><span>{dayLabel(ts)}</span></div>

<style>
  .divider {
    display: flex;
    align-items: center;
    gap: var(--s3);
    margin: var(--s3) var(--s4);
    color: var(--fg-muted);
    font-size: 13px;
  }
  .divider::before,
  .divider::after {
    content: '';
    flex: 1;
    border-top: 1px solid var(--line);
  }
</style>
