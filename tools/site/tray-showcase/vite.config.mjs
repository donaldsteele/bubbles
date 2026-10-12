// Builds tray/frontend against the stub bindings (no Wails). Run from
// tray/frontend: npx vite build --config ../../tools/site/tray-showcase/vite.config.mjs
// The output goes to TRAY_SHOWCASE_OUT (default: a folder next to this file).
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const here = path.dirname(fileURLToPath(import.meta.url));
const stub = path.join(here, 'stub.ts');

export default {
  root: path.resolve(here, '..', '..', '..', 'tray', 'frontend'),
  logLevel: 'warn',
  build: {
    outDir: process.env.TRAY_SHOWCASE_OUT ?? path.join(here, 'out'),
    emptyOutDir: true,
    modulePreload: false,
    cssCodeSplit: false,
  },
  plugins: [
    {
      name: 'tray-showcase-stub',
      enforce: 'pre',
      resolveId(id) {
        return id.endsWith('bindings/github.com/donaldsteele/bubbles/tray') ? stub : null;
      },
    },
  ],
};
