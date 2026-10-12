import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import { tick } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import SecretOnce from '../../src/components/common/SecretOnce.svelte';
import { navigate } from '../../src/lib/router';

afterEach(() => {
  cleanup();
});

function show(ondismiss = vi.fn()) {
  render(SecretOnce, { props: { secret: 'bub_topsecret', title: 'Token', ondismiss } });
  return ondismiss;
}

describe('SecretOnce', () => {
  it('shows the secret with a warning that it is shown once', () => {
    show();
    expect(screen.getByDisplayValue('bub_topsecret')).toBeTruthy();
    expect(screen.getByText(/shown only once/i)).toBeTruthy();
  });

  it('hides the secret for good when the admin confirms', async () => {
    const ondismiss = show();
    await fireEvent.click(screen.getByRole('button', { name: /saved it/i }));
    expect(screen.queryByDisplayValue('bub_topsecret')).toBeNull();
    expect(document.body.textContent).not.toContain('bub_topsecret');
    expect(ondismiss).toHaveBeenCalledTimes(1);
  });

  it('clears on navigation and tells the caller exactly once', async () => {
    const ondismiss = show();
    navigate('/admin/users');
    await tick();
    expect(screen.queryByDisplayValue('bub_topsecret')).toBeNull();
    navigate('/admin/invites');
    expect(ondismiss).toHaveBeenCalledTimes(1);
  });

  it('tells the caller when the page goes away', () => {
    const ondismiss = show();
    cleanup();
    expect(ondismiss).toHaveBeenCalledTimes(1);
  });

  it('copies the secret to the clipboard', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    show();
    await fireEvent.click(screen.getByRole('button', { name: 'Copy' }));
    expect(writeText).toHaveBeenCalledWith('bub_topsecret');
  });
});
