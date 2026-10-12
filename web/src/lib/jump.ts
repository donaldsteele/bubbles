import { parseAround, type Route } from './router';
import { ui } from './state/ui.svelte';

/**
 * Turns "?around=<message_id>" on /c/:id (notification clicks, search results)
 * into a jump request for the message list and removes the query from the
 * address bar so a reload does not jump again. Returns true when it jumped.
 */
export function takeAround(
  route: Route,
  loc: Location = location,
  hist: History = history,
): boolean {
  if (route.name !== 'channel') return false;
  const messageId = parseAround(loc.search);
  if (messageId === null) return false;
  const channelId = route.params['channelId'];
  if (!channelId) return false;
  ui.jump = { channelId, messageId };
  hist.replaceState(null, '', route.path + loc.hash);
  return true;
}
