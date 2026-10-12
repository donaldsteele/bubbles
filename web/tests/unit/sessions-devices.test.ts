import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import SessionsPage from '../../src/components/settings/SessionsPage.svelte';

const apiMock = vi.hoisted(() => ({ GET: vi.fn(), DELETE: vi.fn(), POST: vi.fn() }));

vi.mock('../../src/lib/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../src/lib/api/client')>();
  return { ...actual, api: apiMock };
});

type Device = {
  id: string;
  name: string;
  prefix: string;
  scopes: ('events:read' | 'messages:read' | 'notify')[];
  current: boolean;
  created_at: string;
  last_used_at: string | null;
};

const laptop: Device = {
  id: '01DEVLAPTOP0000000000000000',
  name: 'Laptop',
  prefix: 'bbd_a1b2',
  scopes: ['notify'],
  current: false,
  created_at: '2026-01-02T10:00:00Z',
  last_used_at: null,
};
const desk: Device = {
  id: '01DEVDESK00000000000000000',
  name: 'Desk PC',
  prefix: 'bbd_c3d4',
  scopes: ['events:read', 'messages:read'],
  current: false,
  created_at: '2026-02-03T10:00:00Z',
  last_used_at: '2026-03-04T10:00:00Z',
};

function ok(data: unknown) {
  return { data, response: { ok: true, status: 200 } as unknown as Response };
}

let devices: Device[] = [];

beforeEach(() => {
  devices = [laptop, desk];
  apiMock.GET.mockReset();
  apiMock.DELETE.mockReset();
  apiMock.GET.mockImplementation((path: string) => {
    if (path === '/me/devices') return Promise.resolve(ok({ items: devices }));
    return Promise.resolve(ok({ items: [] }));
  });
});

afterEach(() => {
  cleanup();
});

describe('sessions and devices page', () => {
  it('renders device rows from the mocked API', async () => {
    render(SessionsPage);
    expect(await screen.findByText('Laptop')).toBeTruthy();
    expect(screen.getByText('Desk PC')).toBeTruthy();
    expect(screen.getByText('bbd_a1b2')).toBeTruthy();
    expect(screen.getByText('bbd_c3d4')).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'Sessions & devices' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Sign out device: Laptop' })).toBeTruthy();
  });

  it('shows the empty state when no desktop app is signed in', async () => {
    devices = [];
    render(SessionsPage);
    expect(await screen.findByText('No desktop apps signed in')).toBeTruthy();
  });

  it('revokes a device after confirmation, then refreshes the list', async () => {
    apiMock.DELETE.mockImplementation(() => {
      devices = devices.filter((d) => d.id !== laptop.id);
      return Promise.resolve({ response: { ok: true, status: 204 } as unknown as Response });
    });
    render(SessionsPage);
    await screen.findByText('Laptop');

    await fireEvent.click(screen.getByRole('button', { name: 'Sign out device: Laptop' }));
    expect(screen.getByRole('alertdialog')).toBeTruthy();
    expect(apiMock.DELETE).not.toHaveBeenCalled();

    await fireEvent.click(screen.getByRole('button', { name: 'Sign out device' }));

    expect(apiMock.DELETE).toHaveBeenCalledTimes(1);
    expect(apiMock.DELETE).toHaveBeenCalledWith('/me/devices/{id}', {
      params: { path: { id: laptop.id } },
    });
    await waitFor(() => expect(screen.queryByText('Laptop')).toBeNull());
    expect(screen.getByText('Desk PC')).toBeTruthy();
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });

  it('shows the API error and keeps the row when revoke fails', async () => {
    apiMock.DELETE.mockImplementation(() =>
      Promise.resolve({
        error: { error: { code: 'not_found', message: 'Device not found' } },
        response: { ok: false, status: 404, headers: new Headers() } as unknown as Response,
      }),
    );
    render(SessionsPage);
    await screen.findByText('Laptop');

    await fireEvent.click(screen.getByRole('button', { name: 'Sign out device: Laptop' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Sign out device' }));

    expect(await screen.findByText('Device not found')).toBeTruthy();
    expect(screen.getByText('Laptop')).toBeTruthy();
  });
});
