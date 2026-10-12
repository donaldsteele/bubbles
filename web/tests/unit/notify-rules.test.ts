import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { decide, type NotifyDecision, type NotifyInput } from '../../src/lib/notify/rules';

interface Vector {
  name: string;
  input: NotifyInput;
  want: NotifyDecision;
}

const file = resolve(process.cwd(), '../testdata/notify-rules.json');
const vectors = (JSON.parse(readFileSync(file, 'utf8')) as { vectors: Vector[] }).vectors;

describe('notify rules (shared vectors)', () => {
  it('loads the vectors', () => {
    expect(vectors.length).toBeGreaterThan(50);
  });
  for (const v of vectors) {
    it(v.name, () => {
      expect(decide(v.input)).toEqual(v.want);
    });
  }
});
