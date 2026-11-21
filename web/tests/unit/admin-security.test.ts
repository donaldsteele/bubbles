import { cleanup, fireEvent, render, screen, within } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Schemas } from '../../src/lib/api/client';
import { formatDateTime } from '../../src/lib/admin/format';
import Security from '../../src/routes/admin/Security.svelte';

const apiMock = vi.hoisted(() => ({ GET: vi.fn(), POST: vi.fn() }));

vi.mock('../../src/lib/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../src/lib/api/client')>();
  return { ...actual, api: apiMock };
});

function ok(data: unknown) {
  return { data, response: { ok: true, status: 200 } as unknown as Response };
}

const inDays = (n: number) => new Date(Date.now() + n * 86_400_000).toISOString();

const NAME = 'chat.tail1234.ts.net';
const URL_ = `https://${NAME}:8443/`;

function tailscale(over: Partial<Schemas['AdminTailscale']> = {}): Schemas['AdminTailscale'] {
  return {
    running: true,
    dns_name: NAME,
    url: URL_,
    ips: ['100.101.102.103', 'fd7a:115c:a1e0::1'],
    cert_enabled: false,
    cert_active: false,
    ca_covers_name: true,
    ...over,
  };
}

function status(ts?: Schemas['AdminTailscale'], mode: 'local-ca' | 'byo' = 'local-ca') {
  return {
    mode,
    ca_fingerprint: 'sha256:' + 'ab'.repeat(32),
    leaf_fingerprint: 'sha256:' + 'cd'.repeat(32),
    leaf_not_after: inDays(300),
    sans: ['192.168.1.10', 'bubbles.local'],
    ...(ts ? { tailscale: ts } : {}),
  } satisfies Schemas['AdminTLS'];
}

let tls: Schemas['AdminTLS'];

beforeEach(() => {
  apiMock.GET.mockReset();
  apiMock.POST.mockReset();
  apiMock.GET.mockImplementation((path: string) => {
    if (path === '/admin/tls') return Promise.resolve(ok(tls));
    if (path === '/admin/overview') return Promise.resolve(ok({ onboarding_urls: [] }));
    return Promise.resolve(ok({}));
  });
});

afterEach(() => {
  cleanup();
});

async function open(): Promise<HTMLElement | null> {
  render(Security);
  await screen.findByText('Addresses in the certificate');
  return screen.queryByRole('region', { name: 'Tailscale' });
}

describe('security page: Tailscale section', () => {
  it('is absent when the server reports no tailnet', async () => {
    tls = status();
    expect(await open()).toBeNull();
  });

  it('shows the tailnet name, addresses and URL, and the --tailscale-cert hint when off', async () => {
    tls = status(tailscale());
    const region = (await open()) as HTMLElement;
    const r = within(region);
    expect(r.getByText(`This server is on your tailnet as ${NAME}.`)).toBeTruthy();
    expect(r.getByText('100.101.102.103')).toBeTruthy();
    expect(r.getByText('fd7a:115c:a1e0::1')).toBeTruthy();
    expect(r.getByRole('link', { name: URL_ }).getAttribute('href')).toBe(URL_);
    expect(r.getByText(/Started with --tailscale-cert/)).toBeTruthy();
    expect(r.getByText(/Certificate Transparency logs/)).toBeTruthy();
    expect(r.queryByRole('note')).toBeNull();
  });

  it('copies the tailnet URL', async () => {
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    tls = status(tailscale());
    const region = (await open()) as HTMLElement;
    await fireEvent.click(within(region).getByRole('button', { name: 'Copy the tailnet address' }));
    expect(writeText).toHaveBeenCalledWith(URL_);
    expect(await within(region).findByRole('status')).toBeTruthy();
  });

  it('says when MagicDNS is off', async () => {
    tls = status(tailscale({ dns_name: '', url: '', ca_covers_name: false }));
    const r = within((await open()) as HTMLElement);
    expect(r.getByText(/MagicDNS is off/)).toBeTruthy();
    expect(r.queryByRole('link')).toBeNull();
    // No name, so nothing for the CA to cover.
    expect(r.queryByText(/does not cover/)).toBeNull();
  });

  it('describes an active Tailscale certificate', async () => {
    const until = inDays(80);
    tls = status(tailscale({ cert_enabled: true, cert_active: true, cert_not_after: until }));
    const r = within((await open()) as HTMLElement);
    expect(
      r.getByText(
        `Tailscale certificate served for ${NAME}, valid until ${formatDateTime(until)}. ` +
          `Devices on your tailnet can open ${URL_} without installing the bubbles CA.`,
        { exact: false },
      ),
    ).toBeTruthy();
    expect(r.getByText('79 days left')).toBeTruthy();
    expect(r.queryByText(/Started with --tailscale-cert/)).toBeNull();
    expect(r.queryByRole('note')).toBeNull();
  });

  it('warns with the reason when the Tailscale certificate is not served', async () => {
    const reason =
      'HTTPS certificates are not enabled in this tailnet (Tailscale admin console, DNS, HTTPS Certificates)';
    tls = status(tailscale({ cert_enabled: true, cert_error: reason }));
    const r = within((await open()) as HTMLElement);
    const note = r.getAllByRole('note')[0] as HTMLElement;
    expect(within(note).getByText('The Tailscale certificate is not being served.')).toBeTruthy();
    expect(within(note).getByText(reason)).toBeTruthy();
    expect(r.queryByText(/Started with --tailscale-cert/)).toBeNull();
  });

  it('says Tailscale is not running when --tailscale-cert is on', async () => {
    tls = status(
      tailscale({
        running: false,
        dns_name: '',
        url: '',
        ips: [],
        cert_enabled: true,
        cert_error: 'Tailscale is not running',
      }),
    );
    const r = within((await open()) as HTMLElement);
    expect(r.getByText('Tailscale is not running on this server.')).toBeTruthy();
    expect(r.getByText('The Tailscale certificate is not being served.')).toBeTruthy();
  });

  it('notes when the local CA does not cover the MagicDNS name', async () => {
    tls = status(tailscale({ ca_covers_name: false }));
    const r = within((await open()) as HTMLElement);
    expect(
      r.getByText(
        `The bubbles CA does not cover ${NAME}, so devices that trust it cannot use that name. ` +
          'Use 100.101.102.103 instead, or start the server with --tailscale-cert.',
      ),
    ).toBeTruthy();
  });

  it('drops the CA note once the Tailscale certificate is served', async () => {
    tls = status(
      tailscale({
        ca_covers_name: false,
        cert_enabled: true,
        cert_active: true,
        cert_not_after: inDays(80),
      }),
    );
    const r = within((await open()) as HTMLElement);
    expect(r.queryByText(/does not cover/)).toBeNull();
  });
});
