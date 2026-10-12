<script lang="ts">
  import { ExternalLink, MessageSquare } from '@lucide/svelte';
  import { ackAlert, resolveAlert } from '../../lib/alerts/actions';
  import { alertText } from '../../lib/alerts/text';
  import type { Message } from '../../lib/api/types';
  import { fmt, t } from '../../lib/i18n/en';
  import { relTime } from '../../lib/messages/time';
  import { users } from '../../lib/state/users.svelte';
  import Markdown from '../messages/Markdown.svelte';

  interface Props {
    message: Message;
    /** Hide the "View thread" link (already in the thread). */
    inThread?: boolean;
  }

  let { message, inThread = false }: Props = $props();

  const alert = $derived(message.alert);
  // A resolved alert shows the "ok" legend entry.
  const level = $derived(alert?.state === 'resolved' ? 'ok' : (alert?.severity ?? 'info'));
  const shape = $derived(
    level === 'warning' ? 'triangle' : level === 'critical' ? 'diamond' : 'circle',
  );
  const word = $derived(t.rich.alertSeverity[level]);
  // The body repeats the title, fields and link shown below; keep its text.
  const text = $derived(alertText(message.body));

  const stateText = $derived.by(() => {
    if (!alert) return '';
    if (alert.state === 'resolved') return t.rich.alertResolved;
    if (alert.state === 'acked') {
      const who = alert.acked_by ? users.get(alert.acked_by)?.display_name : '';
      return who ? fmt(t.rich.alertAcked, { name: who }) : t.rich.alertAckedAnon;
    }
    return t.rich.alertOpen;
  });

  /** Only web links are rendered as links. */
  const safeLink = $derived.by(() => {
    if (!alert?.link) return null;
    try {
      const u = new URL(alert.link);
      return u.protocol === 'https:' || u.protocol === 'http:' ? u.href : null;
    } catch {
      return null;
    }
  });

  let busy = $state(false);
  let failed = $state(false);

  async function act(fn: (id: string) => Promise<void>) {
    busy = true;
    failed = false;
    try {
      await fn(message.id);
    } catch {
      failed = true;
    } finally {
      busy = false;
    }
  }
</script>

{#if alert}
  <section class={`card ${level}`} aria-label={`${word}: ${alert.title}`}>
    <header class="head">
      <svg
        class="shape"
        data-shape={shape}
        viewBox="0 0 16 16"
        width="16"
        height="16"
        aria-hidden="true"
        focusable="false"
      >
        {#if shape === 'triangle'}
          <polygon points="8,1 15,14 1,14" fill="currentColor" />
        {:else if shape === 'diamond'}
          <polygon points="8,0.5 15.5,8 8,15.5 0.5,8" fill="currentColor" />
        {:else}
          <circle cx="8" cy="8" r="7" fill="currentColor" />
        {/if}
      </svg>
      <strong class="word">{word}</strong>
      <span class="title">{alert.title}</span>
      <span class="state chip">{stateText}</span>
    </header>

    {#if text}
      <div class="text"><Markdown source={text} /></div>
    {/if}

    {#if alert.fields.length > 0}
      <dl class="fields">
        {#each alert.fields as f, i (i)}
          <div class="field" class:wide={!f.short}>
            <dt>{f.name}</dt>
            <dd>{f.value}</dd>
          </div>
        {/each}
      </dl>
    {/if}

    <p class="meta">
      {fmt(t.rich.alertSeen, { n: String(alert.occurrences), when: relTime(alert.last_seen_at) })}
    </p>

    <div class="row">
      {#if safeLink}
        <a class="btn" href={safeLink} target="_blank" rel="noopener noreferrer">
          <ExternalLink aria-hidden="true" size={14} />
          {t.rich.alertLink}
        </a>
      {/if}
      {#if alert.state === 'open'}
        <button class="btn" type="button" disabled={busy} onclick={() => act(ackAlert)}>
          {t.rich.ack}
        </button>
      {/if}
      {#if alert.state !== 'resolved'}
        <button class="btn" type="button" disabled={busy} onclick={() => act(resolveAlert)}>
          {t.rich.resolve}
        </button>
      {/if}
      {#if !inThread}
        <a class="btn" href={`/c/${message.channel_id}/t/${message.id}`}>
          <MessageSquare aria-hidden="true" size={14} />
          {t.rich.viewThread}
        </a>
      {/if}
    </div>
    {#if failed}<p class="form-error" role="alert">{t.rich.alertFailed}</p>{/if}
  </section>
{/if}

<style>
  .card {
    margin-top: var(--s1);
    padding: var(--s3);
    max-width: 640px;
    background: var(--bg-elev2);
    border: 1px solid var(--line-strong);
    border-left-width: 4px;
    border-radius: var(--radius);
  }
  .card.info {
    border-left-color: var(--info);
  }
  .card.warning {
    border-left-color: var(--warn);
  }
  .card.critical {
    border-left-color: var(--bad);
  }
  .card.ok {
    border-left-color: var(--ok);
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--s2);
  }
  .info .shape {
    color: var(--info);
  }
  .warning .shape {
    color: var(--warn);
  }
  .critical .shape {
    color: var(--bad);
  }
  .ok .shape {
    color: var(--ok);
  }
  .title {
    font-weight: 600;
    flex: 1;
    min-width: 0;
  }
  .chip {
    border: 1px solid var(--line-strong);
    border-radius: 999px;
    padding: 0 var(--s2);
    font-size: 12px;
  }
  .text {
    margin: var(--s2) 0 0;
  }
  .fields {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--s2) var(--s4);
    margin: var(--s2) 0 0;
  }
  .field.wide {
    grid-column: 1 / -1;
  }
  dt {
    color: var(--fg-muted);
    font-size: 12px;
  }
  dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .meta {
    margin: var(--s2) 0 0;
    color: var(--fg-muted);
    font-size: 12px;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--s2);
    margin-top: var(--s2);
  }
  .row .btn {
    display: inline-flex;
    align-items: center;
    gap: var(--s1);
    min-height: 32px;
  }
</style>
