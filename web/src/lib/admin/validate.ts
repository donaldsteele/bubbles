// Client-side validation of the admin forms. The ranges mirror those of the
// server (internal/settings); the server stays the authority and its field
// errors are shown as well.
import { t } from '../i18n/en';

export const LIMITS = {
  instanceNameMax: 64,
  uploadMaxBytes: 2 * 1024 ** 3,
  retentionMaxDays: 3650,
  sessionIdleMaxDays: 90,
  renotifyMin: 10,
  renotifyMax: 86_400,
  inviteMaxUses: 1000,
  inviteMaxDays: 90,
  nameMax: 64,
} as const;

const MB = 1024 * 1024;

export interface SettingsForm {
  instanceName: string;
  openRegistration: boolean;
  uploadMaxMB: string;
  userQuotaMB: string;
  diskReserveMB: string;
  /** Empty = keep messages forever. */
  retentionDays: string;
  webPushEnabled: boolean;
  pushPayload: 'minimal' | 'full';
  /** One CIDR per line (or comma separated). */
  allowedCidrs: string;
  sessionIdleDays: string;
  alertRenotifyS: string;
  mdnsEnabled: boolean;
  webResetEnabled: boolean;
}

function wholeNumber(v: string): number | null {
  const s = v.trim();
  return /^\d{1,15}$/.test(s) ? Number(s) : null;
}

function range(v: string, lo: number, hi: number, message: string): string {
  if (v.trim() === '') return t.common.required;
  const n = wholeNumber(v);
  return n !== null && n >= lo && n <= hi ? '' : message;
}

const CIDR4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(\d{1,2})$/;
const CIDR6 = /^[0-9a-fA-F:]+\/(\d{1,3})$/;

export function isCidr(v: string): boolean {
  const s = v.trim();
  const m4 = CIDR4.exec(s);
  if (m4) {
    return m4.slice(1, 5).every((o) => Number(o) <= 255) && Number(m4[5]) <= 32;
  }
  const m6 = CIDR6.exec(s);
  return !!m6 && s.includes(':') && Number(m6[1]) <= 128;
}

export function parseCidrs(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter((s) => s !== '');
}

/** Field name (as the API names it) to message; empty when everything is valid. */
export function validateSettings(f: SettingsForm): Record<string, string> {
  const e: Record<string, string> = {};
  const name = f.instanceName.trim();
  if (name === '') e['instance_name'] = t.common.required;
  else if ([...name].length > LIMITS.instanceNameMax) e['instance_name'] = t.admin.errInstanceName;
  const up = range(f.uploadMaxMB, 1, LIMITS.uploadMaxBytes / MB, t.admin.errUpload);
  if (up) e['upload_max_bytes'] = up;
  const quota = range(f.userQuotaMB, 0, 100_000_000, t.admin.errNonNegative);
  if (quota) e['user_quota_bytes'] = quota;
  const reserve = range(f.diskReserveMB, 0, 100_000_000, t.admin.errNonNegative);
  if (reserve) e['disk_reserve_bytes'] = reserve;
  if (f.retentionDays.trim() !== '') {
    const r = range(f.retentionDays, 1, LIMITS.retentionMaxDays, t.admin.errRetention);
    if (r) e['default_retention_days'] = r;
  }
  const idle = range(f.sessionIdleDays, 1, LIMITS.sessionIdleMaxDays, t.admin.errSessionIdle);
  if (idle) e['session_idle_days'] = idle;
  const renotify = range(
    f.alertRenotifyS,
    LIMITS.renotifyMin,
    LIMITS.renotifyMax,
    t.admin.errRenotify,
  );
  if (renotify) e['alert_renotify_s'] = renotify;
  const bad = parseCidrs(f.allowedCidrs).filter((c) => !isCidr(c));
  if (bad.length > 0) e['allowed_cidrs'] = `${t.admin.errCidr} ${bad[0] as string}`;
  return e;
}

/** The PATCH body for a valid form. */
export function settingsBody(f: SettingsForm): Record<string, unknown> {
  const retention = f.retentionDays.trim();
  return {
    instance_name: f.instanceName.trim(),
    open_registration: f.openRegistration,
    upload_max_bytes: Number(f.uploadMaxMB.trim()) * MB,
    user_quota_bytes: Number(f.userQuotaMB.trim()) * MB,
    disk_reserve_bytes: Number(f.diskReserveMB.trim()) * MB,
    default_retention_days: retention === '' ? null : Number(retention),
    web_push_enabled: f.webPushEnabled,
    push_payload: f.pushPayload,
    allowed_cidrs: parseCidrs(f.allowedCidrs),
    session_idle_days: Number(f.sessionIdleDays.trim()),
    alert_renotify_s: Number(f.alertRenotifyS.trim()),
    mdns_enabled: f.mdnsEnabled,
    web_reset_enabled: f.webResetEnabled,
  };
}

export interface SettingsWire {
  instance_name: string;
  open_registration: boolean;
  upload_max_bytes: number;
  user_quota_bytes: number;
  disk_reserve_bytes: number;
  default_retention_days: number | null;
  web_push_enabled: boolean;
  push_payload: 'minimal' | 'full';
  allowed_cidrs: string[];
  session_idle_days: number;
  alert_renotify_s: number;
  mdns_enabled: boolean;
  web_reset_enabled: boolean;
}

export function settingsToForm(s: SettingsWire): SettingsForm {
  return {
    instanceName: s.instance_name,
    openRegistration: s.open_registration,
    uploadMaxMB: String(Math.round(s.upload_max_bytes / MB)),
    userQuotaMB: String(Math.round(s.user_quota_bytes / MB)),
    diskReserveMB: String(Math.round(s.disk_reserve_bytes / MB)),
    retentionDays: s.default_retention_days === null ? '' : String(s.default_retention_days),
    webPushEnabled: s.web_push_enabled,
    pushPayload: s.push_payload,
    allowedCidrs: s.allowed_cidrs.join('\n'),
    sessionIdleDays: String(s.session_idle_days),
    alertRenotifyS: String(s.alert_renotify_s),
    mdnsEnabled: s.mdns_enabled,
    webResetEnabled: s.web_reset_enabled,
  };
}

export interface InviteForm {
  role: 'admin' | 'member' | 'guest';
  expiresHours: string;
  maxUses: string;
  note: string;
}

export function validateInvite(f: InviteForm): Record<string, string> {
  const e: Record<string, string> = {};
  const h = range(f.expiresHours, 1, LIMITS.inviteMaxDays * 24, t.admin.errInviteExpiry);
  if (h) e['expires_in'] = h;
  const u = range(f.maxUses, 1, LIMITS.inviteMaxUses, t.admin.errInviteUses);
  if (u) e['max_uses'] = u;
  if ([...f.note].length > 200) e['note'] = t.admin.errNote;
  return e;
}

export function validateName(v: string, max: number = LIMITS.nameMax): string {
  const s = v.trim();
  if (s === '') return t.common.required;
  return [...s].length > max ? t.admin.errNameLong : '';
}

/** A token needs at least one scope. */
export function validateScopes(scopes: string[]): string {
  return scopes.length === 0 ? t.admin.errScopes : '';
}

/** Danger-zone gate: the instance name typed exactly and a password given. */
export function canConfirmReset(instanceName: string, typed: string, password: string): boolean {
  return instanceName !== '' && typed.trim() === instanceName.trim() && password !== '';
}
