import { svelte } from '@sveltejs/vite-plugin-svelte';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { VitePWA } from 'vite-plugin-pwa';
import { defineConfig, type Plugin } from 'vitest/config';

// The server embeds internal/webui/dist with `go:embed all:dist`. Vite empties
// that directory on every build, so the tracked placeholder is re-created once
// the bundle is written. A clean checkout then still builds without Node.
const webuiDist = fileURLToPath(new URL('../internal/webui/dist', import.meta.url));

function keepGitkeep(): Plugin {
  return {
    name: 'bubbles-keep-gitkeep',
    apply: 'build',
    closeBundle() {
      mkdirSync(webuiDist, { recursive: true });
      writeFileSync(join(webuiDist, '.gitkeep'), '');
    },
  };
}

export default defineConfig({
  plugins: [
    svelte(),
    VitePWA({
      strategies: 'injectManifest',
      srcDir: 'src',
      filename: 'sw.ts',
      // The page registers the worker itself (lib/pwa/register.ts): the plugin
      // must not inject an inline script, which the CSP would block.
      injectRegister: false,
      registerType: 'prompt',
      manifest: {
        name: 'bubbles',
        short_name: 'bubbles',
        description: 'Private messaging for your local network',
        display: 'standalone',
        start_url: '/',
        scope: '/',
        theme_color: '#1c1917',
        background_color: '#1c1917',
        icons: [
          { src: '/icons/icon-192.png', sizes: '192x192', type: 'image/png' },
          { src: '/icons/icon-512.png', sizes: '512x512', type: 'image/png' },
          {
            src: '/icons/icon-maskable-512.png',
            sizes: '512x512',
            type: 'image/png',
            purpose: 'maskable',
          },
        ],
      },
      injectManifest: {
        rollupFormat: 'iife',
        globPatterns: ['**/*.{js,css,html,woff2,png,svg}'],
        // Emoji data is large and fetched lazily; the worker caches it at runtime.
        globIgnores: ['emoji/**', 'icons/icon-maskable-512.png'],
      },
    }),
    keepGitkeep(),
  ],
  resolve: {
    // Svelte resolves its browser build for components under test (jsdom).
    conditions: ['browser'],
  },
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
  },
  test: {
    environment: 'jsdom',
    include: ['tests/unit/**/*.test.ts'],
  },
});
