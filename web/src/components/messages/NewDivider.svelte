<script lang="ts">
  import { ArrowDown } from '@lucide/svelte';
  import { t } from '../../lib/i18n/en';
</script>

<div class="divider" id="new-divider">
  <ArrowDown aria-hidden="true" size={14} /><span>{t.chat.newMessages}</span>
</div>

<style>
  .divider {
    display: flex;
    align-items: center;
    gap: var(--s2);
    margin: var(--s3) var(--s4);
    color: var(--accent);
    font-size: 13px;
    font-weight: 700;
  }
  .divider::after {
    content: '';
    flex: 1;
    border-top: 1px solid var(--accent);
  }
</style>
