// markdown-it plugins: @mentions, #channel links and image-to-link. Lookups
// come from env so unknown names stay plain text.
import type MarkdownIt from 'markdown-it';

type Md = InstanceType<typeof MarkdownIt>;

export interface MarkdownEnv {
  /** Resolves a lower-case username to a known user. */
  user?: (username: string) => { id: string } | undefined;
  /** Resolves a lower-case channel name to a known channel. */
  channel?: (name: string) => { id: string } | undefined;
}

const WORD = /[\p{L}\p{N}_]/u;
const MENTION = /^@([a-z0-9][a-z0-9._-]{0,63})/i;
const CHANNEL = /^#([a-z0-9][a-z0-9-]{0,63})/i;

/** True when the character before pos is part of a word (so "a@b" is no mention). */
function afterWord(src: string, pos: number): boolean {
  return pos > 0 && WORD.test(src.charAt(pos - 1));
}

function trimTail(name: string): string {
  return name.replace(/[._-]+$/, '');
}

export function mentionPlugin(md: Md): void {
  md.inline.ruler.push('bubbles_mention', (state, silent) => {
    const { src, pos } = state;
    if (src.charCodeAt(pos) !== 0x40 || afterWord(src, pos)) return false;
    const m = MENTION.exec(src.slice(pos, pos + 66));
    if (!m) return false;
    const name = trimTail(m[1] as string);
    if (!name) return false;
    const lower = name.toLowerCase();
    const env = state.env as MarkdownEnv;
    const broadcast = lower === 'channel' || lower === 'here';
    const user = broadcast ? undefined : env.user?.(lower);
    if (!broadcast && !user) return false; // unknown names stay plain text
    if (!silent) {
      const tok = state.push('bubbles_mention', '', 0);
      tok.content = `@${name}`;
      tok.meta = { userId: user?.id ?? '', broadcast };
    }
    state.pos += name.length + 1;
    return true;
  });
  md.renderer.rules['bubbles_mention'] = (tokens, idx) => {
    const tok = tokens[idx] as unknown as {
      content: string;
      meta: { userId: string; broadcast: boolean };
    };
    const text = md.utils.escapeHtml(tok.content);
    if (tok.meta.broadcast) return `<span class="mention mention-broadcast">${text}</span>`;
    return `<span class="mention" data-user-id="${md.utils.escapeHtml(tok.meta.userId)}">${text}</span>`;
  };
}

export function channelPlugin(md: Md): void {
  md.inline.ruler.push('bubbles_channel', (state, silent) => {
    const { src, pos } = state;
    if (src.charCodeAt(pos) !== 0x23 || afterWord(src, pos)) return false;
    const m = CHANNEL.exec(src.slice(pos, pos + 66));
    if (!m) return false;
    const name = (m[1] as string).replace(/-+$/, '');
    if (!name) return false;
    const ch = (state.env as MarkdownEnv).channel?.(name.toLowerCase());
    if (!ch) return false;
    if (!silent) {
      const tok = state.push('bubbles_channel', '', 0);
      tok.content = `#${name}`;
      tok.meta = { channelId: ch.id };
    }
    state.pos += name.length + 1;
    return true;
  });
  md.renderer.rules['bubbles_channel'] = (tokens, idx) => {
    const tok = tokens[idx] as unknown as { content: string; meta: { channelId: string } };
    const id = md.utils.escapeHtml(tok.meta.channelId);
    return `<a class="chanlink" href="/c/${id}" data-channel-id="${id}">${md.utils.escapeHtml(tok.content)}</a>`;
  };
}

/** Image syntax is never fetched: it renders as a plain link (or text if unsafe). */
export function imagesAsLinksPlugin(md: Md): void {
  md.renderer.rules.image = (tokens, idx) => {
    const tok = tokens[idx] as unknown as { content: string; attrGet(n: string): string | null };
    const src = tok.attrGet('src') ?? '';
    const label = md.utils.escapeHtml(tok.content || src);
    if (!md.validateLink(src)) return label;
    return `<a href="${md.utils.escapeHtml(src)}">${label}</a>`;
  };
}
