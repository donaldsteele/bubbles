<script lang="ts">
  import { t } from '../../lib/i18n/en';
  import type { Route } from '../../lib/router';
  import { settings, type Density, type Theme } from '../../lib/state/settings.svelte';
  import NotificationSettings from './NotificationSettings.svelte';
  import ProfilePage from './ProfilePage.svelte';
  import SessionsPage from './SessionsPage.svelte';

  let { route }: { route: Route } = $props();

  const section = $derived(route.rest === 'profile' || route.rest === 'sessions' ? route.rest : '');

  const themes: { value: Theme; label: string }[] = [
    { value: 'system', label: t.rich.themeSystem },
    { value: 'light', label: t.rich.themeLight },
    { value: 'dark', label: t.rich.themeDark },
  ];
  const densities: { value: Density; label: string }[] = [
    { value: 'comfortable', label: t.rich.densityComfortable },
    { value: 'compact', label: t.rich.densityCompact },
  ];
</script>

<div class="settings">
  <h1 tabindex="-1">{t.settingsPage.title}</h1>
  <nav class="tabs" aria-label={t.rich.settingsNav}>
    <a href="/settings" aria-current={section === '' ? 'page' : undefined}>{t.rich.tabGeneral}</a>
    <a href="/settings/profile" aria-current={section === 'profile' ? 'page' : undefined}
      >{t.rich.tabProfile}</a
    >
    <a href="/settings/sessions" aria-current={section === 'sessions' ? 'page' : undefined}
      >{t.rich.tabSessions}</a
    >
  </nav>

  {#if section === 'profile'}
    <ProfilePage />
  {:else if section === 'sessions'}
    <SessionsPage />
  {:else}
    <section aria-labelledby="appearance-title">
      <h2 id="appearance-title">{t.rich.theme}</h2>
      <fieldset>
        <legend class="visually-hidden">{t.rich.theme}</legend>
        {#each themes as th (th.value)}
          <label class="radio">
            <input
              type="radio"
              name="theme"
              value={th.value}
              checked={settings.theme === th.value}
              onchange={() => settings.setTheme(th.value)}
            />
            {th.label}
          </label>
        {/each}
      </fieldset>

      <h2>{t.rich.density}</h2>
      <fieldset>
        <legend class="visually-hidden">{t.rich.density}</legend>
        {#each densities as d (d.value)}
          <label class="radio">
            <input
              type="radio"
              name="density"
              value={d.value}
              checked={settings.density === d.value}
              onchange={() => settings.setDensity(d.value)}
            />
            {d.label}
          </label>
        {/each}
      </fieldset>

      <label class="radio">
        <input
          type="checkbox"
          checked={settings.enterSends}
          onchange={(e) => settings.setEnterSends(e.currentTarget.checked)}
        />
        {t.rich.enterSends}
      </label>
      <p class="help">{t.rich.enterSendsHelp}</p>
    </section>

    <NotificationSettings />
  {/if}
</div>

<style>
  .settings {
    max-width: 640px;
  }
  h1 {
    margin-top: 0;
    font-size: 22px;
  }
  h2 {
    font-size: 18px;
  }
  .tabs {
    display: flex;
    gap: var(--s3);
    margin-bottom: var(--s4);
    border-bottom: 1px solid var(--line);
  }
  .tabs a {
    display: inline-flex;
    align-items: center;
    min-height: 40px;
    padding: 0 var(--s2);
  }
  .tabs a[aria-current='page'] {
    border-bottom: 2px solid var(--accent);
    font-weight: 600;
  }
  fieldset {
    border: 0;
    padding: 0;
    margin: 0 0 var(--s3);
    display: flex;
    flex-wrap: wrap;
    gap: var(--s3);
  }
  .radio {
    display: inline-flex;
    align-items: center;
    gap: var(--s2);
    min-height: 32px;
  }
  .help {
    margin: 0 0 var(--s4);
    color: var(--fg-muted);
    font-size: 13px;
  }
</style>
