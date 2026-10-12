import type { Schemas } from '../api/client';

export type Scope = Schemas['Scope'];

/** Every token scope; the words next to each checkbox are in t.admin.scopeHints. */
export const SCOPES: Scope[] = [
  'messages:read',
  'messages:write',
  'alerts:write',
  'files:write',
  'channels:read',
  'events:read',
  'commands',
  'dm:write',
  'mcp',
];

/** Toggles v in a list without mutating it. */
export function toggle<T>(list: T[], v: T, on: boolean): T[] {
  const rest = list.filter((x) => x !== v);
  return on ? [...rest, v] : rest;
}
