<script lang="ts">
  import { Pin, SlidersHorizontal, Users } from '@lucide/svelte';
  import type { Channel, Membership } from '../../lib/api/types';
  import { setNotifyLevel, updateMembership } from '../../lib/channel-actions';
  import { channelLabel } from '../../lib/channels';
  import { fmt, t } from '../../lib/i18n/en';
  import { channels } from '../../lib/state/channels.svelte';
  import { session } from '../../lib/state/session.svelte';
  import { ui } from '../../lib/state/ui.svelte';
  import { users } from '../../lib/state/users.svelte';

  let { channel }: { channel: Channel } = $props();

  // Narrow headers fold the member count and the three selects behind a
  // toggle (see the container query below); wide ones always show them.
  let optionsOpen = $state(false);
  $effect(() => {
    void channel.id;
    optionsOpen = false;
  });

  const membership = $derived(channels.memberships[channel.id]);
  const label = $derived(channelLabel(channel, users.byId, session.me?.id ?? ''));
  const isRoom = $derived(channel.kind === 'public' || channel.kind === 'private');

  const FOREVER = '2100-01-01T00:00:00.000Z';
  const mutedMs = $derived(membership?.muted_until ? Date.parse(membership.muted_until) : 0);
  const muted = $derived(mutedMs > Date.now());
  const muteLabel = $derived(
    mutedMs >= Date.parse(FOREVER)
      ? t.rich.mutedForever
      : fmt(t.rich.mutedUntil, { when: new Date(mutedMs).toLocaleString() }),
  );

  async function changeMute(e: Event) {
    const v = (e.currentTarget as HTMLSelectElement).value;
    if (v === 'keep') return;
    const now = Date.now();
    const until =
      v === '1h'
        ? new Date(now + 3_600_000).toISOString()
        : v === '24h'
          ? new Date(now + 86_400_000).toISOString()
          : v === 'forever'
            ? FOREVER
            : null;
    try {
      await updateMembership(channel.id, { muted_until: until });
    } catch {
      // the select snaps back to the stored state on the next render
    }
  }

  async function changeMinSeverity(e: Event) {
    const v = (e.currentTarget as HTMLSelectElement).value as Membership['alert_min_severity'];
    try {
      await updateMembership(channel.id, { alert_min_severity: v });
    } catch {
      // the select snaps back to the stored level on the next render
    }
  }

  function togglePanel(which: 'pins' | 'members', e: MouseEvent) {
    if (ui.panel === which) {
      ui.panel = null;
      return;
    }
    ui.drawerTrigger = e.currentTarget as HTMLElement;
    ui.panel = which;
  }

  async function changeLevel(e: Event) {
    const level = (e.currentTarget as HTMLSelectElement).value as Membership['notify_level'];
    try {
      await setNotifyLevel(channel.id, level);
    } catch {
      // the select snaps back to the stored level on the next render
    }
  }
</script>

<header class="head">
  <div class="bar">
    <div class="title">
      <h1 tabindex="-1">{isRoom ? '#' : ''}{label}</h1>
      {#if isRoom}
        <p class="topic">{channel.topic || t.chat.noTopic}</p>
      {/if}
    </div>
    <div class="tools" id="channel-options" class:open={optionsOpen}>
      <span class="count">
        <Users aria-hidden="true" size={16} />
        {channel.member_count === 1
          ? t.chat.memberCountOne
          : fmt(t.chat.memberCount, { n: String(channel.member_count) })}
      </span>
      {#if membership}
        <label class="notify">
          <span class="visually-hidden">{t.chat.notifications}</span>
          <select
            value={membership.notify_level}
            onchange={changeLevel}
            title={t.chat.notifications}
          >
            <option value="all">{t.chat.notifyAll}</option>
            <option value="mentions">{t.chat.notifyMentions}</option>
            <option value="none">{t.chat.notifyNone}</option>
          </select>
        </label>
        <label class="notify">
          <span class="visually-hidden">{t.rich.alertMin}</span>
          <select
            value={membership.alert_min_severity}
            onchange={changeMinSeverity}
            title={t.rich.alertMin}
          >
            <option value="info">{t.rich.alertMin} {t.rich.alertSeverity.info}</option>
            <option value="warning">{t.rich.alertMin} {t.rich.alertSeverity.warning}</option>
            <option value="critical">{t.rich.alertMin} {t.rich.alertSeverity.critical}</option>
          </select>
        </label>
        <label class="notify">
          <span class="visually-hidden">{t.rich.mute}</span>
          <select value={muted ? 'keep' : 'none'} onchange={changeMute} title={t.rich.mute}>
            <option value="none">{t.rich.muteNone}</option>
            {#if muted}<option value="keep">{muteLabel}</option>{/if}
            <option value="1h">{t.rich.mute}: {t.rich.mute1h}</option>
            <option value="24h">{t.rich.mute}: {t.rich.muteDay}</option>
            <option value="forever">{t.rich.mute}: {t.rich.muteForever}</option>
          </select>
        </label>
      {/if}
    </div>
    <div class="actions">
      <button
        class="icon more"
        type="button"
        aria-label={t.chat.channelOptions}
        title={t.chat.channelOptions}
        aria-expanded={optionsOpen}
        aria-controls="channel-options"
        onclick={() => (optionsOpen = !optionsOpen)}
      >
        <SlidersHorizontal aria-hidden="true" size={16} />
      </button>
      <button
        class="icon"
        type="button"
        aria-label={t.chat.pins}
        title={t.chat.pins}
        aria-pressed={ui.panel === 'pins'}
        onclick={(e) => togglePanel('pins', e)}
      >
        <Pin aria-hidden="true" size={16} />
      </button>
      <button
        class="icon"
        type="button"
        aria-label={t.chat.members}
        title={t.chat.members}
        aria-pressed={ui.panel === 'members'}
        onclick={(e) => togglePanel('members', e)}
      >
        <Users aria-hidden="true" size={16} />
      </button>
    </div>
  </div>
</header>

<style>
  .head {
    container-type: inline-size;
    border-bottom: 1px solid var(--line);
  }
  .bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--s2) var(--s3);
    padding: var(--s2) var(--s4);
  }
  .title {
    flex: 1 1 12rem;
    min-width: 0;
  }
  h1 {
    margin: 0;
    font-size: 18px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .topic {
    margin: 0;
    color: var(--fg-muted);
    font-size: 13px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tools,
  .actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--s2);
    max-width: 100%;
  }
  .count {
    display: inline-flex;
    align-items: center;
    gap: var(--s1);
    color: var(--fg-muted);
    font-size: 13px;
  }
  select {
    min-height: 32px;
    background: var(--bg-elev2);
    border: 1px solid var(--line-strong);
    border-radius: var(--radius);
    padding: 0 var(--s2);
  }
  .icon {
    min-width: 32px;
    min-height: 32px;
    display: grid;
    place-items: center;
    background: none;
    border: 1px solid var(--line);
    border-radius: var(--radius);
  }
  .icon:disabled {
    opacity: 0.5;
  }
  .more {
    display: none;
  }

  /* Narrow: one row (title, then the buttons); the count and the selects
     open on a row of their own from the settings toggle. */
  @container (max-width: 879px) {
    .title {
      flex-basis: 0;
    }
    .actions {
      flex-wrap: nowrap;
    }
    .more {
      display: grid;
    }
    .more[aria-expanded='true'] {
      background: var(--bg-elev2);
    }
    .tools {
      display: none;
      order: 1;
      flex-basis: 100%;
    }
    .tools.open {
      display: flex;
    }
  }

  /* Phone: the navigation toggle sits at the top left, so the row starts
     after it, and the buttons are touch-sized. */
  @media (max-width: 639px) {
    .bar {
      min-height: 60px;
      padding-left: 72px;
    }
    .icon {
      min-width: 44px;
      min-height: 44px;
    }
    select {
      min-height: 44px;
    }
  }
</style>
