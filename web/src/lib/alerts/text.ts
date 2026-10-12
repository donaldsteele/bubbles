// An alert message's body is the Markdown fallback the server renders for
// clients without alert cards (internal/alerts/render.go): the title in bold,
// the text, a "| Field | Value |" table and an "[Open link](<…>)" line, joined
// by blank lines. The alert card shows the title, fields and link from the
// structured alert, so it only needs the text part.

const tableHead = '| Field | Value |';
const linkHead = '[Open link](<';

/** The free text of an alert body, without the title, field table and link. */
export function alertText(body: string): string {
  const parts = body.split('\n\n');
  if (parts.length > 0 && /^\*\*[^\n]*\*\*$/.test(parts[0])) parts.shift();
  if (parts.length > 0 && parts[parts.length - 1].startsWith(linkHead)) parts.pop();
  if (parts.length > 0 && parts[parts.length - 1].startsWith(tableHead)) parts.pop();
  return parts.join('\n\n').trim();
}
