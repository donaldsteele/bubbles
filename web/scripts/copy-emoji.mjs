// Builds public/emoji/data.json from the pinned emojibase-data package so the
// emoji picker never loads data from a CDN. Compact shape:
//   { groups: string[], items: { e, n, g, s, t, k? }[] }
// e = emoji, n = label, g = group index, s = shortcodes, t = search tags,
// k = single-tone skin variants for tones 1..5 (omitted when absent).
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const base = dirname(require.resolve('emojibase-data/package.json'));
const read = (p) => JSON.parse(readFileSync(join(base, p), 'utf8'));

const data = read('en/data.json');
const github = read('en/shortcodes/github.json');
const cldr = read('en/shortcodes/cldr.json');
const groupNames = read('meta/groups.json').groups;

const list = (v) => (v === undefined ? [] : Array.isArray(v) ? v : [v]);
const COMPONENT = 2;

const items = [];
for (const d of data) {
  if (d.group === undefined || d.group === COMPONENT) continue;
  const s = [...new Set([...list(github[d.hexcode]), ...list(cldr[d.hexcode])])];
  const item = { e: d.emoji, n: d.label, g: d.group, s, t: d.tags ?? [] };
  const k = [];
  for (const sk of d.skins ?? []) {
    if (typeof sk.tone === 'number') k[sk.tone - 1] = sk.emoji;
  }
  if (k.length === 5 && k.every(Boolean)) item.k = k;
  items.push({ order: d.order, item });
}
items.sort((a, b) => a.order - b.order);

const groups = Object.keys(groupNames)
  .sort((a, b) => Number(a) - Number(b))
  .map((key) => groupNames[key]);

const out = fileURLToPath(new URL('../public/emoji/data.json', import.meta.url));
mkdirSync(dirname(out), { recursive: true });
writeFileSync(out, JSON.stringify({ groups, items: items.map((x) => x.item) }));
console.log(`copy-emoji: ${items.length} emoji`);
