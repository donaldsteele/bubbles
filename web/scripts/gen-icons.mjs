// Renders src/assets/logo.svg into the committed PNG icons in public/icons/.
// Dev-only: run `node scripts/gen-icons.mjs` after changing the logo.
import { Resvg } from '@resvg/resvg-js';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const svg = readFileSync(join(root, 'src/assets/logo.svg'));
const out = join(root, 'public/icons');
mkdirSync(out, { recursive: true });

// The logo is full-bleed with its artwork inside the maskable safe zone, so one
// source serves every purpose.
const sizes = {
  'icon-192.png': 192,
  'icon-512.png': 512,
  'icon-maskable-512.png': 512,
  'apple-touch-icon.png': 180,
};

for (const [name, size] of Object.entries(sizes)) {
  const png = new Resvg(svg, { fitTo: { mode: 'width', value: size } }).render().asPng();
  writeFileSync(join(out, name), png);
  console.log(`gen-icons: ${name} (${size}x${size}, ${png.length} bytes)`);
}
