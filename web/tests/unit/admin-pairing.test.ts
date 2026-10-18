import { cleanup, fireEvent, render, screen, within } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { alertsBotWithoutChannel, createChannelByDefault } from '../../src/lib/admin/pairing';
import type { Schemas } from '../../src/lib/api/client';
import type { Channel } from '../../src/lib/api/types';
import Pairing from '../../src/routes/admin/Pairing.svelte';

const apiMock = vi.hoisted(() => ({ GET: vi.fn(), POST: vi.fn() }));

vi.mock('../../src/lib/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../src/lib/api/client')>();
  return { ...actual, api: apiMock };
});

function channel(id: string, name: string, over: Partial<Channel> = {}): Channel {
  return {
    id,
    kind: 'public',
    name,
    topic: '',
    description: '',
    is_default: false,
    retention_days: null,
    created_by: null,
    created_at: '2026-01-02T10:00:00Z',
    archived_at: null,
    member_count: 1,
    ...over,
  };
}

const ops = channel('01CHANOPS00000000000000000', 'ops');
const old = channel('01CHANOLD00000000000000000', 'old', { archived_at: '2026-02-01T10:00:00Z' });

function request(scopes: Schemas['Scope'][]): Schemas['PendingPairing'] {
  return {
    id: '01PAIRREQ00000000000000000',
    app_name: 'netdiag',
    description: 'sends alerts',
    requested_scopes: scopes,
    code: '123456',
    source_ip: '192.168.1.9',
    ca_fingerprint: 'sha256:' + 'ab'.repeat(32),
    created_at: '2026-01-02T10:00:00Z',
    expires_at: '2026-01-02T10:10:00Z',
  };
}

function ok(data: unknown) {
  return { data, response: { ok: true, status: 200 } as unknown as Response };
}

let pending: Schemas['PendingPairing'][] = [];

beforeEach(() => {
  apiMock.GET.mockReset();
  apiMock.POST.mockReset();
  apiMock.GET.mockImplementation((path: string) => {
    if (path === '/admin/pairing-requests') return Promise.resolve(ok({ items: pending }));
    if (path === '/admin/channels') return Promise.resolve(ok({ items: [ops, old] }));
    return Promise.resolve(ok({ items: [] }));
  });
});

afterEach(() => {
  cleanup();
});

const warning = /bot will be in no channel/;

async function openApproval(scopes: Schemas['Scope'][]) {
  pending = [request(scopes)];
  render(Pairing);
  const req = await screen.findByRole('article', { name: 'netdiag' });
  await fireEvent.click(within(req).getByRole('button', { name: 'Approve' }));
  const create = within(req).getByRole('checkbox', {
    name: 'Also create a channel for this app',
  }) as HTMLInputElement;
  const submit = within(req)
    .getAllByRole('button', { name: 'Approve' })
    .at(-1) as HTMLButtonElement;
  return { req, create, submit };
}

describe('pairing approval choices', () => {
  it('creates a channel by default only for an app that asks for alerts:write', () => {
    expect(createChannelByDefault(['alerts:write'])).toBe(true);
    expect(createChannelByDefault(['messages:read', 'alerts:write'])).toBe(true);
    expect(createChannelByDefault(['messages:write'])).toBe(false);
    expect(createChannelByDefault([])).toBe(false);
  });

  it('warns only when alerts:write leaves the bot in no usable channel', () => {
    const chans = [ops, old];
    expect(alertsBotWithoutChannel(['alerts:write'], [], chans, false)).toBe(true);
    expect(alertsBotWithoutChannel(['alerts:write'], [], chans, true)).toBe(false);
    expect(alertsBotWithoutChannel(['alerts:write'], [ops.id], chans, false)).toBe(false);
    // An archived channel is not a place to post alerts.
    expect(alertsBotWithoutChannel(['alerts:write'], [old.id], chans, false)).toBe(true);
    expect(alertsBotWithoutChannel(['messages:write'], [], chans, false)).toBe(false);
  });
});

describe('pairing approval form', () => {
  it('starts with "Also create a channel" ticked when alerts:write is requested', async () => {
    const { create, submit } = await openApproval(['alerts:write']);
    expect(create.checked).toBe(true);
    expect(screen.queryByText(warning)).toBeNull();
    expect(submit.getAttribute('aria-describedby')).toBeNull();
  });

  it('starts unticked for an app that does not ask for alerts:write', async () => {
    const { create } = await openApproval(['messages:write']);
    expect(create.checked).toBe(false);
    expect(screen.queryByText(warning)).toBeNull();
  });

  it('warns next to the approve button when the bot would be in no channel, without blocking', async () => {
    const { req, create, submit } = await openApproval(['alerts:write']);
    await fireEvent.click(create);
    expect(create.checked).toBe(false);
    const note = within(req).getByRole('note');
    expect(note.textContent).toMatch(warning);
    expect(submit.getAttribute('aria-describedby')).toBe(note.id);
    expect(submit.disabled).toBe(false);

    // Ticking an existing channel clears the warning, unticking brings it back.
    const opsBox = within(req).getByRole('checkbox', { name: '#ops' });
    await fireEvent.click(opsBox);
    expect(within(req).queryByRole('note')).toBeNull();
    await fireEvent.click(opsBox);
    expect(within(req).getByRole('note')).toBeTruthy();

    // The admin may still approve.
    apiMock.POST.mockResolvedValue(ok({ app: { name: 'netdiag' }, channel_ids: [] }));
    await fireEvent.click(submit);
    expect(apiMock.POST).toHaveBeenCalledWith('/admin/pairing-requests/{id}/approve', {
      params: { path: { id: '01PAIRREQ00000000000000000' } },
      body: { scopes: ['alerts:write'], channel_ids: [], create_channel: false },
    });
  });

  it('warns when the admin grants alerts:write that was not requested', async () => {
    const { req } = await openApproval(['messages:write']);
    expect(within(req).queryByRole('note')).toBeNull();
    await fireEvent.click(within(req).getByRole('checkbox', { name: /^alerts:write/ }));
    expect(within(req).getByRole('note').textContent).toMatch(warning);
  });
});
