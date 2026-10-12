// Sidebar grouping and display names for channels and conversations.
import type { Channel, User } from './api/types';
import { fmt, t } from './i18n/en';

export interface Groups {
  channels: Channel[];
  dms: Channel[];
}

/** Display name: channel name, or the other participants of a DM. */
export function channelLabel(ch: Channel, users: Record<string, User>, meId: string): string {
  if (ch.kind === 'public' || ch.kind === 'private') return ch.name ?? ch.id;
  const others = (ch.participant_ids ?? []).filter((id) => id !== meId);
  if (others.length === 0) {
    const me = users[meId];
    return fmt(t.chat.noteToSelf, { name: me?.display_name || me?.username || '' }).trim();
  }
  return others
    .map((id) => users[id]?.display_name || users[id]?.username || id)
    .sort((a, b) => a.localeCompare(b))
    .join(', ');
}

/** Joined, non-archived channels split into Channels and Direct messages. */
export function groupChannels(
  joined: Channel[],
  users: Record<string, User>,
  meId: string,
): Groups {
  const live = joined.filter((c) => c.archived_at === null);
  const label = (c: Channel) => channelLabel(c, users, meId).toLowerCase();
  const cmp = (a: Channel, b: Channel) => label(a).localeCompare(label(b));
  return {
    channels: live.filter((c) => c.kind === 'public' || c.kind === 'private').sort(cmp),
    dms: live.filter((c) => c.kind === 'dm' || c.kind === 'group_dm').sort(cmp),
  };
}

/** Sidebar order: channels first, then conversations. */
export function orderedIds(g: Groups): string[] {
  return [...g.channels, ...g.dms].map((c) => c.id);
}
