import { cleanup, render, screen } from '@testing-library/svelte';
import { afterEach, describe, expect, it } from 'vitest';
import Login from '../../src/routes/Login.svelte';
import NotFound from '../../src/routes/NotFound.svelte';

describe('routes', () => {
  afterEach(() => {
    cleanup();
  });

  it('NotFound renders a level-1 heading inside main', () => {
    render(NotFound);
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('Page not found');
    expect(screen.getByRole('main')).toBeTruthy();
  });

  it('Login labels its fields', () => {
    render(Login);
    expect(screen.getByLabelText('Username')).toBeTruthy();
    expect(screen.getByLabelText('Password')).toBeTruthy();
  });
});
