import { describe, expect, it } from 'vitest';
import { alertText } from '../../src/lib/alerts/text';

describe('alertText', () => {
  it('keeps only the text of a full alert body', () => {
    const body = [
      '**Printer offline**',
      'Brother HL-L2350 stopped answering.\n\nLast seen 2 minutes ago.',
      '| Field | Value |\n| --- | --- |\n| IP | 192.168.1.50 |',
      '[Open link](<https://192.168.1.50/>)',
    ].join('\n\n');
    expect(alertText(body)).toBe('Brother HL-L2350 stopped answering.\n\nLast seen 2 minutes ago.');
  });

  it('returns nothing for a title-only alert', () => {
    expect(alertText('**Toner low: black 15%**')).toBe('');
  });

  it('handles fields without text, and a link without fields', () => {
    expect(alertText('**T**\n\n| Field | Value |\n| --- | --- |\n| A | b |')).toBe('');
    expect(alertText('**T**\n\nsome text\n\n[Open link](<https://x.lan/>)')).toBe('some text');
  });

  it('leaves a body that is not in the alert format alone', () => {
    expect(alertText('plain *markdown* text')).toBe('plain *markdown* text');
  });
});
