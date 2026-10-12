import type { SettingsPublic } from '../api/types';
import { KEYS, readItem, writeItem } from '../storage';

export type Theme = 'dark' | 'light' | 'system';
export type Density = 'comfortable' | 'compact';

const THEMES: readonly Theme[] = ['dark', 'light', 'system'];
const DENSITIES: readonly Density[] = ['comfortable', 'compact'];

function systemPrefersLight(): boolean {
  try {
    return window.matchMedia('(prefers-color-scheme: light)').matches;
  } catch {
    return false;
  }
}

/** Per-viewer preferences (localStorage) plus the server's public settings. */
class SettingsState {
  theme = $state<Theme>('system');
  viewer = $state('anon');
  /** Enter sends (true) or inserts a newline (false; Shift+Enter then sends). */
  enterSends = $state(true);
  density = $state<Density>('comfortable');
  /** Play a chime for notifications. */
  sound = $state(true);
  /** Show browser notifications (needs the browser permission as well). */
  browserNotify = $state(false);
  /** Unix ms until which all notifications are paused; 0 when not paused. */
  pausedUntil = $state(0);
  public = $state<SettingsPublic | null>(null);

  get instanceName(): string {
    return this.public?.instance_name ?? '';
  }

  /** Switches to a viewer ('anon' before sign-in) and loads their stored prefs. */
  setViewer(id: string): void {
    this.viewer = id;
    this.theme = 'system';
    this.enterSends = true;
    this.density = 'comfortable';
    this.sound = true;
    this.browserNotify = false;
    this.pausedUntil = 0;
    const raw = readItem(KEYS.settings(id));
    if (raw !== null) {
      try {
        const v = JSON.parse(raw) as Record<string, unknown>;
        if (THEMES.includes(v.theme as Theme)) this.theme = v.theme as Theme;
        if (typeof v['enterSends'] === 'boolean') this.enterSends = v['enterSends'];
        if (DENSITIES.includes(v['density'] as Density)) this.density = v['density'] as Density;
        if (typeof v['sound'] === 'boolean') this.sound = v['sound'];
        if (typeof v['browserNotify'] === 'boolean') this.browserNotify = v['browserNotify'];
        if (typeof v['pausedUntil'] === 'number') this.pausedUntil = v['pausedUntil'];
      } catch {
        // corrupt entry: keep defaults
      }
    }
    this.apply();
  }

  setTheme(theme: Theme): void {
    this.theme = theme;
    this.#persist();
    this.apply();
  }

  setEnterSends(v: boolean): void {
    this.enterSends = v;
    this.#persist();
  }

  setDensity(v: Density): void {
    this.density = v;
    this.#persist();
    this.apply();
  }

  setSound(v: boolean): void {
    this.sound = v;
    this.#persist();
  }

  setBrowserNotify(v: boolean): void {
    this.browserNotify = v;
    this.#persist();
  }

  /** Pauses notifications until the given time (unix ms); 0 resumes. */
  setPausedUntil(ms: number): void {
    this.pausedUntil = ms;
    this.#persist();
  }

  #persist(): void {
    writeItem(
      KEYS.settings(this.viewer),
      JSON.stringify({
        theme: this.theme,
        enterSends: this.enterSends,
        density: this.density,
        sound: this.sound,
        browserNotify: this.browserNotify,
        pausedUntil: this.pausedUntil,
      }),
    );
  }

  /** Writes the resolved theme onto <html data-theme>. */
  apply(): void {
    const resolved =
      this.theme === 'system' ? (systemPrefersLight() ? 'light' : 'dark') : this.theme;
    document.documentElement.dataset.theme = resolved;
    document.documentElement.dataset.density = this.density;
  }
}

export const settings = new SettingsState();
