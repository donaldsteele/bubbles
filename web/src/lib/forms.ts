import { ApiError } from './api/client';
import { t } from './i18n/en';

const USERNAME_RE = /^[a-z0-9._-]{2,32}$/;

/** Returns an error string or '' when the username is acceptable. */
export function validateUsername(v: string): string {
  const u = v.trim().toLowerCase();
  if (u === '') return t.common.required;
  return USERNAME_RE.test(u) ? '' : t.validation.username;
}

export function validatePassword(v: string): string {
  if (v === '') return t.common.required;
  if (v.length < 10) return t.validation.passwordShort;
  if (v.length > 128) return t.validation.passwordLong;
  return '';
}

export function validateRequired(v: string): string {
  return v.trim() === '' ? t.common.required : '';
}

export interface FormFailure {
  /** Field name (as the API names it) to message. */
  fields: Record<string, string>;
  /** Message for the whole form, shown above the submit button. */
  form: string;
}

/** Splits an ApiError into inline field errors and a form-level message. */
export function failureFrom(err: unknown, byStatus: Record<number, string> = {}): FormFailure {
  if (!(err instanceof ApiError)) return { fields: {}, form: t.common.genericError };
  const fields = err.fields;
  if (Object.keys(fields).length > 0) return { fields, form: '' };
  if (err.status === 0) return { fields: {}, form: t.common.networkError };
  if (err.status === 429) return { fields: {}, form: t.common.rateLimited };
  return { fields: {}, form: byStatus[err.status] ?? err.message ?? t.common.genericError };
}
