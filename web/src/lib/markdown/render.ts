// Markdown to sanitized HTML. The only place that produces HTML for {@html}
// (Markdown.svelte). markdown-it runs with html:false, links are restricted to
// http/https/mailto and the result goes through DOMPurify.
import DOMPurify from 'dompurify';
import MarkdownIt from 'markdown-it';
import { channelPlugin, imagesAsLinksPlugin, mentionPlugin, type MarkdownEnv } from './plugins';

export type { MarkdownEnv } from './plugins';

const SAFE_LINK = /^(?:https?|mailto):/i;

const md = new MarkdownIt({ html: false, breaks: true, linkify: true, typographer: false });
md.linkify.set({ fuzzyLink: false, fuzzyIP: false });
md.validateLink = (url: string) => SAFE_LINK.test(url.trim());
md.use(mentionPlugin).use(channelPlugin).use(imagesAsLinksPlugin);

const ALLOWED_TAGS = [
  'p',
  'br',
  'strong',
  'em',
  's',
  'del',
  'code',
  'pre',
  'blockquote',
  'ul',
  'ol',
  'li',
  'h1',
  'h2',
  'h3',
  'h4',
  'h5',
  'h6',
  'hr',
  'a',
  'span',
  'table',
  'thead',
  'tbody',
  'tr',
  'th',
  'td',
];
const ALLOWED_ATTR = ['href', 'title', 'class', 'data-user-id', 'data-channel-id', 'target', 'rel'];
const ALLOWED_URI = /^(?:(?:https?|mailto):|\/c\/[\w-]+$)/i;

DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  if (node.tagName !== 'A') return;
  if (node.hasAttribute('data-channel-id')) {
    node.removeAttribute('target');
    node.removeAttribute('rel');
    return;
  }
  node.setAttribute('target', '_blank');
  node.setAttribute('rel', 'noopener noreferrer nofollow');
});

export function renderMarkdown(source: string, env: MarkdownEnv = {}): string {
  const raw = md.render(source, env as never);
  return DOMPurify.sanitize(raw, {
    ALLOWED_TAGS,
    ALLOWED_ATTR,
    ALLOWED_URI_REGEXP: ALLOWED_URI,
    ALLOW_DATA_ATTR: false,
    ADD_URI_SAFE_ATTR: ['data-user-id', 'data-channel-id'],
    RETURN_TRUSTED_TYPE: false,
  });
}
