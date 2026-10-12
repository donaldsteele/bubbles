// The Danger-zone actions. A factory reset signs this tab out (the server
// closes the socket with 4010 and forgets every session), so its outcome - the
// new setup token - lives here, outside any page, and App shows it.
import { api, ApiError, call, isResetting, type Schemas } from '../api/client';
import { t } from '../i18n/en';
import { holdResetNavigation } from '../reset';
import { session } from '../state/session.svelte';

export type FactoryResult = Schemas['FactoryReset'];

export interface ResetOptions {
  mode: 'content' | 'factory';
  confirm: string;
  password: string;
  rotateCa: boolean;
  keepSettings: boolean;
}

/** Message for a failed reset request (wrong confirmation or password, limits, maintenance). */
export function resetErrorMessage(err: unknown): string {
  if (!(err instanceof ApiError)) return t.common.genericError;
  if (isResetting(err)) return t.danger.resetting;
  switch (err.status) {
    case 403:
      return err.message || t.danger.forbidden;
    case 429:
      return t.danger.tooManyAttempts;
    case 409:
      return err.message || t.danger.busy;
    default:
      return err.status === 0 ? t.common.networkError : err.message || t.common.genericError;
  }
}

class ResetFlow {
  phase = $state<'idle' | 'working' | 'done' | 'failed'>('idle');
  factory = $state<FactoryResult | null>(null);
  error = $state('');
  /** A factory reset request is in flight. */
  running = $state(false);

  /** The result screen is up, or the reset signed this tab out and is still running. */
  get active(): boolean {
    return this.phase !== 'idle' || (this.running && !session.authenticated);
  }

  /**
   * Runs the reset. Content: resolves with the new epoch. Factory: resolves
   * with null; the answer is in `factory` / `phase`. A refusal before anything
   * was deleted (wrong password, rate limit) is thrown to the caller.
   */
  async run(o: ResetOptions): Promise<number | null> {
    const factory = o.mode === 'factory';
    if (factory) {
      holdResetNavigation(true);
      this.running = true;
    }
    try {
      const res = await call(
        api.POST('/admin/reset', {
          body: {
            mode: o.mode,
            confirm: o.confirm,
            password: o.password,
            rotate_ca: factory && o.rotateCa,
            keep_settings: factory && o.keepSettings,
          },
        }),
      );
      if (!factory) return (res as Schemas['ContentCleared']).epoch;
      this.factory = res as FactoryResult;
      this.phase = 'done';
      this.running = false;
      holdResetNavigation(false, false);
      return null;
    } catch (err) {
      this.running = false;
      holdResetNavigation(false, false);
      // Signed out meanwhile: the page that could show the error is gone.
      if (factory && !session.authenticated) {
        this.error = resetErrorMessage(err);
        this.phase = 'failed';
        return null;
      }
      throw err;
    }
  }

  /** Closes the result screen (and forgets the setup token). */
  dismiss(): void {
    this.factory = null;
    this.error = '';
    this.phase = 'idle';
  }

  /** Where the setup wizard of the reset server is. */
  setupHref(): string {
    const base = this.factory?.setup_url;
    try {
      return base ? new URL('setup', base).href : '/setup';
    } catch {
      return '/setup';
    }
  }
}

export const resetFlow = new ResetFlow();
