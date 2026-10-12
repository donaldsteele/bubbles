import { api, call } from '../api/client';
import type { Message } from '../api/types';
import { messages } from '../state/messages.svelte';

export type AlertSeverity = 'info' | 'warning' | 'critical';

export async function ackAlert(id: string): Promise<void> {
  messages.upsert(await call(api.POST('/alerts/{id}/ack', { params: { path: { id } } })));
}

export async function resolveAlert(id: string): Promise<void> {
  messages.upsert(
    await call(api.POST('/alerts/{id}/resolve', { params: { path: { id } }, body: {} })),
  );
}

export async function listAlerts(
  state: 'open' | 'acked',
  cursor?: string,
): Promise<{ items: Message[]; next: string | null }> {
  const r = await call(
    api.GET('/alerts', { params: { query: { state, ...(cursor ? { cursor } : {}), limit: 50 } } }),
  );
  return { items: r.items, next: r.next_cursor };
}

const RANK: Record<AlertSeverity, number> = { info: 0, warning: 1, critical: 2 };

/** True when the alert is at least as severe as the filter (all = no filter). */
export function meetsSeverity(sev: AlertSeverity, min: AlertSeverity | 'all'): boolean {
  return min === 'all' || RANK[sev] >= RANK[min];
}
