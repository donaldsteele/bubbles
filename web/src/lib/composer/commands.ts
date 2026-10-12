// Slash commands typed in the composer.
export interface ParsedCommand {
  name: string;
  args: string;
}

/**
 * A message that starts with "/name" where name is a known command is a
 * command invocation. Anything else ("/ hi", "/unknown", "a /topic b") is a
 * normal message.
 */
export function parseCommand(text: string, known: readonly string[]): ParsedCommand | null {
  const m = /^\/([A-Za-z0-9_-]+)(?:\s+([\s\S]*))?$/.exec(text.trim());
  if (!m) return null;
  const name = (m[1] as string).toLowerCase();
  if (!known.some((k) => k.toLowerCase() === name)) return null;
  return { name, args: (m[2] ?? '').trim() };
}
