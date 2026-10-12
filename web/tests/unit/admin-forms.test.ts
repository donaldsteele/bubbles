import { describe, expect, it } from 'vitest';
import { daysUntil, formatBytes, formatUptime, groupFingerprint } from '../../src/lib/admin/format';
import {
  canConfirmReset,
  isCidr,
  parseCidrs,
  settingsBody,
  settingsToForm,
  validateInvite,
  validateName,
  validateScopes,
  validateSettings,
  type SettingsForm,
} from '../../src/lib/admin/validate';

const good: SettingsForm = {
  instanceName: 'Home',
  openRegistration: false,
  registrationWindowHours: 24,
  uploadMaxMB: '100',
  userQuotaMB: '5120',
  diskReserveMB: '1024',
  retentionDays: '',
  webPushEnabled: false,
  pushPayload: 'minimal',
  allowedCidrs: '',
  sessionIdleDays: '30',
  alertRenotifyS: '1800',
  mdnsEnabled: true,
  webResetEnabled: true,
};

describe('validateSettings', () => {
  it('accepts the defaults', () => {
    expect(validateSettings(good)).toEqual({});
  });

  it.each([
    ['instance_name', { instanceName: '  ' }],
    ['instance_name', { instanceName: 'x'.repeat(65) }],
    ['upload_max_bytes', { uploadMaxMB: '0' }],
    ['upload_max_bytes', { uploadMaxMB: '2049' }],
    ['upload_max_bytes', { uploadMaxMB: '' }],
    ['upload_max_bytes', { uploadMaxMB: '1.5' }],
    ['user_quota_bytes', { userQuotaMB: '-1' }],
    ['disk_reserve_bytes', { diskReserveMB: 'lots' }],
    ['default_retention_days', { retentionDays: '0' }],
    ['default_retention_days', { retentionDays: '3651' }],
    ['session_idle_days', { sessionIdleDays: '91' }],
    ['session_idle_days', { sessionIdleDays: '0' }],
    ['alert_renotify_s', { alertRenotifyS: '9' }],
    ['alert_renotify_s', { alertRenotifyS: '86401' }],
    ['allowed_cidrs', { allowedCidrs: '10.0.0.0/8, nope' }],
  ] as [string, Partial<SettingsForm>][])('rejects %s', (field, patch) => {
    const errors = validateSettings({ ...good, ...patch });
    expect(errors[field]).toBeTruthy();
  });

  it('accepts boundary values and an empty (forever) retention', () => {
    const edge = {
      ...good,
      uploadMaxMB: '2048',
      userQuotaMB: '0',
      retentionDays: '3650',
      sessionIdleDays: '90',
      alertRenotifyS: '10',
    };
    expect(validateSettings(edge)).toEqual({});
  });
});

describe('settings wire conversion', () => {
  it('turns megabytes into bytes and an empty retention into null', () => {
    const body = settingsBody({ ...good, allowedCidrs: '100.64.0.0/10\n192.168.0.0/16' });
    expect(body).toMatchObject({
      instance_name: 'Home',
      upload_max_bytes: 100 * 1024 * 1024,
      user_quota_bytes: 5120 * 1024 * 1024,
      default_retention_days: null,
      allowed_cidrs: ['100.64.0.0/10', '192.168.0.0/16'],
      session_idle_days: 30,
      web_reset_enabled: true,
    });
    expect(settingsBody({ ...good, retentionDays: '30' })['default_retention_days']).toBe(30);
  });

  it('round-trips through the form', () => {
    const wire = {
      instance_name: 'Home',
      open_registration: true,
      registration_window_hours: 24,
      upload_max_bytes: 100 * 1024 * 1024,
      user_quota_bytes: 0,
      disk_reserve_bytes: 1024 ** 3,
      default_retention_days: 14,
      web_push_enabled: true,
      push_payload: 'full' as const,
      allowed_cidrs: ['100.64.0.0/10'],
      session_idle_days: 7,
      alert_renotify_s: 60,
      mdns_enabled: false,
      web_reset_enabled: false,
    };
    const form = settingsToForm(wire);
    expect(form.retentionDays).toBe('14');
    expect(validateSettings(form)).toEqual({});
    expect(settingsBody(form)).toEqual(wire);
  });
});

describe('cidr', () => {
  it.each(['10.0.0.0/8', '192.168.1.0/24', '100.64.0.0/10', 'fd00::/8', '::1/128'])(
    'accepts %s',
    (c) => {
      expect(isCidr(c)).toBe(true);
    },
  );
  it.each(['10.0.0.0', '10.0.0.0/33', '300.1.1.1/8', 'abc/8', 'fd00::/129', ''])(
    'rejects %s',
    (c) => {
      expect(isCidr(c)).toBe(false);
    },
  );
  it('splits on commas, spaces and newlines', () => {
    expect(parseCidrs('a, b\nc  d,,')).toEqual(['a', 'b', 'c', 'd']);
  });
});

describe('invite and token forms', () => {
  const inv = { role: 'member' as const, expiresHours: '72', maxUses: '1', note: '' };
  it('accepts defaults and limits', () => {
    expect(validateInvite(inv)).toEqual({});
    expect(validateInvite({ ...inv, expiresHours: '2160', maxUses: '1000' })).toEqual({});
  });
  it.each([
    ['expires_in', { expiresHours: '0' }],
    ['expires_in', { expiresHours: '2161' }],
    ['expires_in', { expiresHours: '' }],
    ['max_uses', { maxUses: '0' }],
    ['max_uses', { maxUses: '1001' }],
    ['note', { note: 'x'.repeat(201) }],
  ] as [string, Partial<typeof inv>][])('rejects %s', (field, patch) => {
    expect(validateInvite({ ...inv, ...patch })[field]).toBeTruthy();
  });
  it('needs a name and at least one scope', () => {
    expect(validateName('  ')).toBeTruthy();
    expect(validateName('netdiag')).toBe('');
    expect(validateName('x'.repeat(65))).toBeTruthy();
    expect(validateScopes([])).toBeTruthy();
    expect(validateScopes(['mcp'])).toBe('');
  });
});

describe('canConfirmReset', () => {
  it('needs the exact instance name and a password', () => {
    expect(canConfirmReset('Home', 'Home', 'pw')).toBe(true);
    expect(canConfirmReset('Home', ' Home ', 'pw')).toBe(true);
    expect(canConfirmReset('Home', 'home', 'pw')).toBe(false);
    expect(canConfirmReset('Home', 'Home', '')).toBe(false);
    expect(canConfirmReset('Home', '', 'pw')).toBe(false);
    expect(canConfirmReset('', '', 'pw')).toBe(false);
  });
});

describe('format helpers', () => {
  it('groups a fingerprint by 4 characters', () => {
    expect(groupFingerprint('sha256:ab:cd:ef:01:23')).toBe('ABCD EF01 23');
    expect(groupFingerprint('')).toBe('');
    expect(groupFingerprint('0123456789abcdef')).toBe('0123 4567 89AB CDEF');
  });
  it('formats bytes and uptime', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(1536)).toBe('1.5 KB');
    expect(formatBytes(5 * 1024 ** 3)).toBe('5.0 GB');
    expect(formatUptime(0)).toBe('0s');
    expect(formatUptime(65)).toBe('1m 5s');
    expect(formatUptime(90_000)).toBe('1d 1h');
  });
  it('counts days until a date', () => {
    const now = Date.parse('2026-01-01T00:00:00Z');
    expect(daysUntil('2026-01-31T00:00:00Z', now)).toBe(30);
    expect(daysUntil('2025-12-30T00:00:00Z', now)).toBe(-2);
  });
});
