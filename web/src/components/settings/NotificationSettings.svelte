<script lang="ts">
  import { onMount } from 'svelte';
  import {
    browserPermission,
    isPaused,
    pauseUntil,
    requestBrowserPermission,
    type BrowserPermission,
    type PauseKind,
  } from '../../lib/notify/browser';
  import { playChime } from '../../lib/notify/sound';
  import { fmt, t } from '../../lib/i18n/en';
  import {
    pushAvailability,
    currentSubscription,
    subscribePush,
    unsubscribePush,
    type PushAvailability,
  } from '../../lib/push/subscribe';
  import { settings } from '../../lib/state/settings.svelte';

  let permission = $state<BrowserPermission>(browserPermission());
  let push = $state<PushAvailability | 'checking'>('checking');
  let subscribed = $state(false);
  let pushError = $state(false);
  let busy = $state(false);

  onMount(() => {
    void (async () => {
      push = await pushAvailability();
      if (push === 'available') subscribed = (await currentSubscription()) !== null;
    })();
  });

  // The permission prompt is only ever shown from this click.
  async function enableBrowser() {
    permission = await requestBrowserPermission();
    settings.setBrowserNotify(permission === 'granted');
  }

  function pause(kind: PauseKind) {
    settings.setPausedUntil(pauseUntil(kind, Date.now()));
  }

  async function togglePush(e: Event) {
    const on = (e.currentTarget as HTMLInputElement).checked;
    busy = true;
    pushError = false;
    try {
      if (on) {
        if (browserPermission() !== 'granted') permission = await requestBrowserPermission();
        subscribed = (await subscribePush()) && browserPermission() === 'granted';
        if (!subscribed) await unsubscribePush();
      } else {
        await unsubscribePush();
        subscribed = false;
      }
    } catch {
      pushError = true;
      subscribed = (await currentSubscription().catch(() => null)) !== null;
    } finally {
      busy = false;
    }
  }

  const pausedText = $derived(
    isPaused()
      ? fmt(t.rich.pausedUntil, {
          when: new Date(settings.pausedUntil).toLocaleString([], {
            weekday: 'short',
            hour: '2-digit',
            minute: '2-digit',
          }),
        })
      : '',
  );
</script>

<section aria-labelledby="notif-title">
  <h2 id="notif-title">{t.rich.notifTitle}</h2>

  <div class="block">
    <h3>{t.rich.notifBrowser}</h3>
    <p class="help">{t.rich.notifBrowserHelp}</p>
    {#if permission === 'unsupported'}
      <p>{t.rich.notifUnsupported}</p>
    {:else if permission === 'denied'}
      <p>{t.rich.notifDenied}</p>
    {:else if permission === 'granted' && settings.browserNotify}
      <p>{t.rich.notifGranted}</p>
      <button class="btn" type="button" onclick={() => settings.setBrowserNotify(false)}>
        {t.rich.notifOff}
      </button>
    {:else}
      <button class="btn primary" type="button" onclick={enableBrowser}>
        {t.rich.notifEnable}
      </button>
    {/if}
  </div>

  <div class="block">
    <label class="check">
      <input
        type="checkbox"
        checked={settings.sound}
        onchange={(e) => settings.setSound(e.currentTarget.checked)}
      />
      {t.rich.soundOn}
    </label>
    <button class="btn" type="button" onclick={playChime}>▶</button>
  </div>

  <div class="block">
    <h3>{t.rich.pause}</h3>
    {#if isPaused()}
      <p role="status">{pausedText}</p>
      <button class="btn" type="button" onclick={() => settings.setPausedUntil(0)}>
        {t.rich.pauseResume}
      </button>
    {:else}
      <div class="row">
        <button class="btn" type="button" onclick={() => pause('1h')}>{t.rich.pauseHour}</button>
        <button class="btn" type="button" onclick={() => pause('tomorrow')}>
          {t.rich.pauseTomorrow}
        </button>
      </div>
    {/if}
  </div>

  <div class="block">
    <h3>{t.rich.push}</h3>
    {#if push === 'available'}
      <label class="check">
        <input type="checkbox" checked={subscribed} disabled={busy} onchange={togglePush} />
        {t.rich.pushOn}
      </label>
      {#if pushError}<p class="form-error" role="alert">{t.rich.pushFailed}</p>{/if}
    {:else if push === 'unavailable'}
      <p>{t.rich.pushUnavailable}</p>
    {/if}
  </div>
</section>

<style>
  h2 {
    font-size: 18px;
  }
  h3 {
    font-size: 15px;
    margin: 0 0 var(--s1);
  }
  .block {
    margin-bottom: var(--s4);
  }
  .help {
    margin: 0 0 var(--s2);
    color: var(--fg-muted);
    font-size: 13px;
  }
  .check {
    display: inline-flex;
    align-items: center;
    gap: var(--s2);
    min-height: 32px;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--s2);
  }
</style>
