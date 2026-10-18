import { cleanup, fireEvent, render, screen, within } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  alertsBotWithoutChannel,
  alertsChannelName,
  appUsername,
  createChannelByDefault,
  joinableChannels,
} from '../../src/lib/admin/pairing';
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

function request(
  scopes: Schemas['Scope'][],
  replaces?: Schemas['PairingReplaces'],
): Schemas['PendingPairing'] {
  return {
    ...(replaces ? { replaces } : {}),
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
let listed: Channel[] = [];

beforeEach(() => {
  listed = [ops, old];
  apiMock.GET.mockReset();
  apiMock.POST.mockReset();
  apiMock.GET.mockImplementation((path: string) => {
    if (path === '/admin/pairing-requests') return Promise.resolve(ok({ items: pending }));
    if (path === '/admin/channels') return Promise.resolve(ok({ items: listed }));
    return Promise.resolve(ok({ items: [] }));
  });
});

afterEach(() => {
  cleanup();
});

const warning = /bot will be in no channel/;

async function openApproval(scopes: Schemas['Scope'][], replaces?: Schemas['PairingReplaces']) {
  pending = [request(scopes, replaces)];
  render(Pairing);
  const req = await screen.findByRole('article', { name: 'netdiag' });
  // The mocked requests resolve at once: one macrotask turn lets the channel
  // list arrive before the form opens, as the pre-tick depends on it.
  await new Promise((r) => setTimeout(r, 0));
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
  it('derives the username and alerts channel name the way the server does', () => {
    expect(appUsername('netdiag')).toBe('netdiag');
    expect(appUsername('  Hall Display!! ')).toBe('hall-display');
    expect(appUsername('net.diag_2')).toBe('net.diag_2');
    expect(appUsername('x')).toBe('app-x');
    expect(appUsername('!!')).toBe('app');
    expect(appUsername('a'.repeat(31) + '-bc')).toBe('a'.repeat(31));
    expect(alertsChannelName('netdiag')).toBe('netdiag-alerts');
    expect(alertsChannelName('Hall display')).toBe('hall-display-alerts');
    expect(alertsChannelName('net.diag_2')).toBe('net-diag-2-alerts');
  });

  it('creates a channel by default only for an app that asks for alerts:write', () => {
    const chans = [ops, old];
    expect(createChannelByDefault(['alerts:write'], 'netdiag', chans)).toBe(true);
    expect(createChannelByDefault(['messages:read', 'alerts:write'], 'netdiag', chans)).toBe(true);
    expect(createChannelByDefault(['messages:write'], 'netdiag', chans)).toBe(false);
    expect(createChannelByDefault([], 'netdiag', chans)).toBe(false);
  });

  it('does not create a channel by default when its name is taken', () => {
    const taken = channel('01CHANNDA00000000000000000', 'netdiag-alerts');
    expect(createChannelByDefault(['alerts:write'], 'netdiag', [ops, taken])).toBe(false);
    // Names are unique across private and archived channels too.
    const priv = { ...taken, kind: 'private' as const };
    expect(createChannelByDefault(['alerts:write'], 'NetDiag', [priv])).toBe(false);
    const gone = { ...taken, archived_at: '2026-02-01T10:00:00Z' };
    expect(createChannelByDefault(['alerts:write'], 'netdiag', [gone])).toBe(false);
    // A DM never holds a channel name.
    const dm = { ...taken, kind: 'dm' as const };
    expect(createChannelByDefault(['alerts:write'], 'netdiag', [dm])).toBe(true);
  });

  it('offers only channels that are not archived', () => {
    expect(joinableChannels([ops, old])).toEqual([ops]);
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

  it('starts unticked and warns when #<app>-alerts already exists', async () => {
    listed = [ops, channel('01CHANNDA00000000000000000', 'netdiag-alerts')];
    const { req, create } = await openApproval(['alerts:write']);
    expect(create.checked).toBe(false);
    expect(within(req).getByRole('note').textContent).toMatch(warning);
    // Ticking the existing alerts channel instead clears the warning.
    await fireEvent.click(within(req).getByRole('checkbox', { name: '#netdiag-alerts' }));
    expect(within(req).queryByRole('note')).toBeNull();
  });

  it('does not offer archived channels', async () => {
    const { req } = await openApproval(['alerts:write']);
    expect(within(req).getByRole('checkbox', { name: '#ops' })).toBeTruthy();
    expect(within(req).queryByRole('checkbox', { name: '#old' })).toBeNull();
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

const alertsChan = channel('01CHANNDA00000000000000000', 'netdiag-alerts');

function replaces(over: Partial<Schemas['PairingReplaces']> = {}): Schemas['PairingReplaces'] {
  return {
    app_id: '01APPNETDIAG000000000000000',
    name: 'netdiag',
    username: 'netdiag',
    disabled_at: null,
    channels: [{ id: alertsChan.id, name: 'netdiag-alerts' }],
    ...over,
  };
}

describe('reconnecting an existing app', () => {
  it('uses the existing bot for the defaults', () => {
    const inChannel = replaces();
    const noChannel = replaces({ channels: [] });
    // A bot that is already in a channel needs no new one and no warning.
    expect(createChannelByDefault(['alerts:write'], 'netdiag', [ops], inChannel)).toBe(false);
    expect(alertsBotWithoutChannel(['alerts:write'], [], [ops], false, inChannel)).toBe(false);
    // Without a channel it behaves like a new app, named after its username.
    expect(createChannelByDefault(['alerts:write'], 'netdiag', [ops], noChannel)).toBe(true);
    expect(alertsBotWithoutChannel(['alerts:write'], [], [ops], false, noChannel)).toBe(true);
    const lab = replaces({ username: 'net.lab', channels: [] });
    expect(createChannelByDefault(['alerts:write'], 'netdiag', [alertsChan], lab)).toBe(true);
    const labChan = channel('01CHANLAB000000000000000000', 'net-lab-alerts');
    expect(createChannelByDefault(['alerts:write'], 'netdiag', [labChan], lab)).toBe(false);
  });

  it('shows what the request replaces and reconnects by default', async () => {
    listed = [ops, old, alertsChan];
    const { req, create, submit } = await openApproval(['alerts:write'], replaces());
    expect(req.textContent).toContain(
      'Replaces netdiag (@netdiag). Its bot is in #netdiag-alerts.',
    );
    const reconnect = within(req).getByRole('checkbox', {
      name: 'Reconnect as the existing app',
    }) as HTMLInputElement;
    expect(reconnect.checked).toBe(true);
    expect(reconnect.getAttribute('aria-describedby')).toBeTruthy();
    expect(req.textContent).toContain(
      'Keeps its bot and channels. Its old tokens stop working when the app connects.',
    );
    // The bot is already in a channel: nothing to create, nothing to warn about.
    expect(create.checked).toBe(false);
    expect(within(req).queryByRole('note')).toBeNull();

    apiMock.POST.mockResolvedValue(ok({ app: { name: 'netdiag' }, channel_ids: [] }));
    await fireEvent.click(submit);
    expect(apiMock.POST).toHaveBeenCalledWith('/admin/pairing-requests/{id}/approve', {
      params: { path: { id: '01PAIRREQ00000000000000000' } },
      body: { scopes: ['alerts:write'], channel_ids: [], create_channel: false, replace_app: true },
    });
  });

  it('approves as a new app when the admin unticks reconnecting', async () => {
    listed = [ops, old, alertsChan];
    const { req, create, submit } = await openApproval(['alerts:write'], replaces());
    await fireEvent.click(
      within(req).getByRole('checkbox', { name: 'Reconnect as the existing app' }),
    );
    // A new bot is in no channel, and #netdiag-alerts is taken: the base form's warning.
    expect(create.checked).toBe(false);
    expect(within(req).getByRole('note').textContent).toMatch(warning);

    apiMock.POST.mockResolvedValue(ok({ app: { name: 'netdiag' }, channel_ids: [] }));
    await fireEvent.click(submit);
    expect(apiMock.POST).toHaveBeenCalledWith('/admin/pairing-requests/{id}/approve', {
      params: { path: { id: '01PAIRREQ00000000000000000' } },
      body: {
        scopes: ['alerts:write'],
        channel_ids: [],
        create_channel: false,
        replace_app: false,
      },
    });
  });

  it('says when the app is disabled and that reconnecting enables it', async () => {
    const { req, create } = await openApproval(
      ['alerts:write'],
      replaces({ disabled_at: '2026-01-01T10:00:00Z', channels: [] }),
    );
    expect(req.textContent).toContain(
      'Replaces netdiag (@netdiag), which is disabled. Its bot is in no channel.',
    );
    expect(req.textContent).toContain('Reconnecting enables it again.');
    // The bot is in no channel and #netdiag-alerts is free: ticked, as for a new app.
    expect(create.checked).toBe(true);
  });
});
