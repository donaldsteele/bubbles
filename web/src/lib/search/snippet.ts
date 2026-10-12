// The server escapes result text and only emits <mark>. It is sanitized again
// here so a server bug can never put markup into the page.
import DOMPurify from 'dompurify';

export function sanitizeSnippet(snippet: string): string {
  return DOMPurify.sanitize(snippet, { ALLOWED_TAGS: ['mark'], ALLOWED_ATTR: [] });
}

export interface SearchFilters {
  channel: string;
  from: string;
  has: '' | 'file' | 'image' | 'link';
  before: string;
  after: string;
}

export const emptyFilters: SearchFilters = {
  channel: '',
  from: '',
  has: '',
  before: '',
  after: '',
};

/** Joins free text and filter chips into the server's query grammar. */
export function buildQuery(text: string, f: SearchFilters): string {
  const parts = [text.trim()];
  if (f.channel) parts.push(`in:#${f.channel}`);
  if (f.from) parts.push(`from:@${f.from}`);
  if (f.has) parts.push(`has:${f.has}`);
  if (/^\d{4}-\d{2}-\d{2}$/.test(f.before)) parts.push(`before:${f.before}`);
  if (/^\d{4}-\d{2}-\d{2}$/.test(f.after)) parts.push(`after:${f.after}`);
  return parts.filter(Boolean).join(' ');
}
