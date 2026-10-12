import { describe, expect, it } from 'vitest';
import { renderMarkdown, type MarkdownEnv } from '../../src/lib/markdown/render';

const env: MarkdownEnv = {
  user: (n) => (n === 'ana' ? { id: 'U1' } : undefined),
  channel: (n) => (n === 'general' ? { id: 'C1' } : undefined),
};

function dom(html: string): HTMLElement {
  const d = document.createElement('div');
  d.innerHTML = html;
  return d;
}

describe('renderMarkdown', () => {
  it('strips script tags and raw HTML', () => {
    const out = renderMarkdown(
      'hi <script>alert(1)</script> <img src=x onerror=alert(1)> <b>x</b>',
    );
    const d = dom(out);
    expect(d.querySelector('script,img,b')).toBeNull();
    expect(d.querySelector('[onerror]')).toBeNull();
    expect(d.textContent).toContain('<script>'); // shown as text, never executed
  });

  it.each(['javascript:alert(1)', 'data:text/html,hi', 'vbscript:x', 'JaVaScRiPt:alert(1)'])(
    'does not link %s',
    (url) => {
      const out = renderMarkdown(`[click](${url})`);
      expect(dom(out).querySelector('a')).toBeNull();
      expect(out.toLowerCase()).not.toContain('href');
    },
  );

  it('links http(s) and mailto with safe attributes', () => {
    const a = dom(
      renderMarkdown('see https://example.com/x and [m](mailto:a@b.co)'),
    ).querySelectorAll('a');
    expect(a).toHaveLength(2);
    for (const el of a) {
      expect(el.getAttribute('rel')).toBe('noopener noreferrer nofollow');
      expect(el.getAttribute('target')).toBe('_blank');
    }
  });

  it('renders image syntax as a plain link', () => {
    const d = dom(renderMarkdown('![cat](https://example.com/cat.png)'));
    expect(d.querySelector('img')).toBeNull();
    const a = d.querySelector('a');
    expect(a?.getAttribute('href')).toBe('https://example.com/cat.png');
    expect(a?.textContent).toBe('cat');
    expect(dom(renderMarkdown('![x](javascript:alert(1))')).querySelector('a')).toBeNull();
  });

  it('turns @mentions of known users into spans, leaves unknown as text', () => {
    const d = dom(renderMarkdown('hello @ana and @ghost and a@ana.test', env));
    const spans = d.querySelectorAll('span.mention');
    expect(spans).toHaveLength(1);
    expect(spans[0]?.getAttribute('data-user-id')).toBe('U1');
    expect(d.textContent).toContain('@ghost');
  });

  it('highlights @channel and @here', () => {
    const d = dom(renderMarkdown('@channel and @here', env));
    expect(d.querySelectorAll('span.mention-broadcast')).toHaveLength(2);
  });

  it('links #channel only for known channels', () => {
    const d = dom(renderMarkdown('see #general and #nowhere', env));
    const links = d.querySelectorAll('a.chanlink');
    expect(links).toHaveLength(1);
    expect(links[0]?.getAttribute('href')).toBe('/c/C1');
    expect(links[0]?.getAttribute('target')).toBeNull();
    expect(d.textContent).toContain('#nowhere');
  });

  it('keeps basic formatting and code blocks without a highlighter', () => {
    const out = renderMarkdown('**b** _i_ `c`\n\n```js\nlet x = 1;\n```');
    expect(out).toContain('<strong>b</strong>');
    expect(out).toContain('<em>i</em>');
    expect(out).toContain('<pre><code');
    expect(out).not.toContain('hljs');
  });
});
