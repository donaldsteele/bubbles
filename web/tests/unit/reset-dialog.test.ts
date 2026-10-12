import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import ResetDialog from '../../src/components/admin/ResetDialog.svelte';
import { backupFileName } from '../../src/lib/admin/backup';
import { resetErrorMessage } from '../../src/lib/admin/resetFlow.svelte';
import { ApiError } from '../../src/lib/api/client';
import { holdResetNavigation, resetLocalState, setHardNavigate } from '../../src/lib/reset';
import { resetNotice } from '../../src/lib/state/resetNotice.svelte';

afterEach(() => {
  cleanup();
});

function open(mode: 'content' | 'factory') {
  const onclose = vi.fn();
  render(ResetDialog, { props: { mode, instanceName: 'Home', onclose, oncleared: vi.fn() } });
  return onclose;
}

const confirmButton = () => screen.getByRole('button', { name: /^Delete (content|everything)$/ });
const typed = () => screen.getByLabelText(/Type the instance name/) as HTMLInputElement;
const password = () => screen.getByLabelText('Your password') as HTMLInputElement;

describe('reset dialog', () => {
  it('is an alertdialog with the danger button disabled until both fields are filled', async () => {
    open('factory');
    expect(screen.getByRole('alertdialog')).toBeTruthy();
    expect((confirmButton() as HTMLButtonElement).disabled).toBe(true);

    await fireEvent.input(typed(), { target: { value: 'Hom' } });
    await fireEvent.input(password(), { target: { value: 'secret pass' } });
    expect((confirmButton() as HTMLButtonElement).disabled).toBe(true);

    await fireEvent.input(typed(), { target: { value: 'Home' } });
    expect((confirmButton() as HTMLButtonElement).disabled).toBe(false);

    await fireEvent.input(password(), { target: { value: '' } });
    expect((confirmButton() as HTMLButtonElement).disabled).toBe(true);
  });

  it('matches the instance name exactly (case matters)', async () => {
    open('content');
    await fireEvent.input(typed(), { target: { value: 'home' } });
    await fireEvent.input(password(), { target: { value: 'pw' } });
    expect((confirmButton() as HTMLButtonElement).disabled).toBe(true);
  });

  it('uses the word Delete in the confirm button and shows the instance name to type', () => {
    open('content');
    expect(confirmButton().textContent).toContain('Delete');
    expect(document.body.textContent).toContain('Home');
  });

  it('offers CA and settings choices for a factory reset only, both off', () => {
    open('factory');
    const ca = screen.getByLabelText(/Generate new CA/) as HTMLInputElement;
    const keep = screen.getByLabelText('Keep server settings') as HTMLInputElement;
    expect(ca.checked).toBe(false);
    expect(keep.checked).toBe(false);
    cleanup();
    open('content');
    expect(screen.queryByLabelText(/Generate new CA/)).toBeNull();
    expect(screen.queryByLabelText('Keep server settings')).toBeNull();
  });

  it('asks for a passphrase when the backup is requested and gates on it', async () => {
    open('content');
    expect(screen.queryByLabelText('Backup passphrase')).toBeNull();
    await fireEvent.click(screen.getByLabelText('Download encrypted backup first'));
    await fireEvent.input(typed(), { target: { value: 'Home' } });
    await fireEvent.input(password(), { target: { value: 'pw' } });
    expect((confirmButton() as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.input(screen.getByLabelText('Backup passphrase'), { target: { value: 'p' } });
    expect((confirmButton() as HTMLButtonElement).disabled).toBe(false);
  });

  it('closes on Cancel', async () => {
    const onclose = open('content');
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onclose).toHaveBeenCalled();
  });
});

describe('reset errors', () => {
  it('explains a 503 resetting, a refusal and the rate limit', () => {
    expect(resetErrorMessage(new ApiError(503, 'resetting', 'resetting'))).toMatch(
      /busy resetting/,
    );
    expect(resetErrorMessage(new ApiError(403, 'forbidden', 'the password is incorrect'))).toBe(
      'the password is incorrect',
    );
    expect(resetErrorMessage(new ApiError(429, 'rate_limited', 'slow'))).toMatch(/Too many/);
    expect(resetErrorMessage(new ApiError(0, 'network', 'network error'))).toMatch(/reach/);
    expect(resetErrorMessage(new Error('x'))).toMatch(/Something went wrong/);
  });

  it('names the backup file from Content-Disposition', () => {
    expect(backupFileName('attachment; filename="bubbles-20260101-101010.bbk"')).toBe(
      'bubbles-20260101-101010.bbk',
    );
    expect(backupFileName('attachment; filename="../../evil.bbk"')).toBe('....evil.bbk');
    expect(backupFileName(null, new Date(2026, 0, 2))).toBe('bubbles-20260102.bbk');
  });
});

describe('server reset notice', () => {
  const nav = vi.fn();
  beforeEach(() => {
    nav.mockReset();
    setHardNavigate(nav);
    resetNotice.dismiss();
    sessionStorage.clear();
  });

  it('is raised by a reset this device did not start', async () => {
    await resetLocalState('content');
    expect(resetNotice.level).toBe('content');
    await resetLocalState('factory');
    expect(resetNotice.level).toBe('factory');
    expect(nav).toHaveBeenCalledWith('/');
    // A factory reset reloads the page: the notice is picked up again.
    resetNotice.dismiss();
    sessionStorage.setItem('bubbles-reset-notice', 'factory');
    resetNotice.restore();
    expect(resetNotice.level).toBe('factory');
    expect(sessionStorage.getItem('bubbles-reset-notice')).toBeNull();
  });

  it('is not raised, and the page stays, for the admin who started the reset', async () => {
    holdResetNavigation(true);
    await resetLocalState('factory');
    expect(resetNotice.level).toBeNull();
    expect(nav).not.toHaveBeenCalled();
    holdResetNavigation(false, false);
    expect(nav).not.toHaveBeenCalled();
  });
});
