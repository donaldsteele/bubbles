# Dependencies

Every direct dependency of the project, with the exact pinned version and its license.
Transitive packages are pinned by `go.sum` and `web/package-lock.json`.

## Go (server, SDK, tools)

| Module | Version | License | Purpose |
|--------|---------|---------|---------|
| `golang.org/x/sys` | v0.49.0 | BSD-3-Clause | Windows ACLs (`SetNamedSecurityInfo`), `LockFileEx` single-instance lock and the Windows service API (`svc`, `svc/mgr`; `internal/service`) |
| `filippo.io/age` | v1.3.2 | BSD-3-Clause | Passphrase (scrypt) encryption of backup archives (`internal/backup`); pulls in `filippo.io/hpke` v0.4.0 (BSD-3-Clause) |
| `golang.org/x/term` | v0.47.0 | BSD-3-Clause | No-echo passphrase prompt for `bubbles backup` / `restore` when stdin is a terminal |
| `modernc.org/sqlite` | v1.60.1 | BSD-3-Clause | Pure-Go (CGO-free) SQLite driver: WAL, FTS5, `VACUUM INTO` snapshots (`internal/store`) |
| `github.com/oklog/ulid/v2` | v2.1.2 | Apache-2.0 | Monotonic ULID generation in the store writer goroutine |
| `github.com/skip2/go-qrcode` | v0.0.0-20200617195104-da1b6568686e | MIT | QR code matrices for the onboarding page's SVG codes (`internal/onboarding`); no tagged release exists, so the latest commit is pinned |
| `golang.org/x/crypto` | v0.58.0 | BSD-3-Clause | `argon2` (argon2id password hashing, `internal/auth`) |
| `github.com/yuin/goldmark` | v1.8.6 | MIT | CommonMark parser (GFM extensions); `internal/chat` derives a message's searchable `body_plain` and its `@mentions` from the AST so code spans and code blocks never mention anyone |
| `github.com/coder/websocket` | v1.8.15 | ISC | WebSocket server for the realtime endpoint `GET /api/v1/ws` (`internal/wsgateway`); also used by its tests as the client |
| `golang.org/x/image` | v0.47.0 | BSD-3-Clause | `webp` decoder (WebP uploads, `internal/files`) and the `draw.CatmullRom` scaler behind the 640 px thumbnails; JPEG, PNG and GIF come from the standard library |
| `github.com/SherClockHolmes/webpush-go` | v1.4.0 | MIT | Web Push sender (`internal/push`): RFC 8291 payload encryption (aes128gcm) and the VAPID (RFC 8292) JWT; also pulls in `github.com/golang-jwt/jwt/v5` v5.2.1 (MIT) for the VAPID signature. Used with an injected, address-guarded HTTP client |
| `github.com/getkin/kin-openapi` | v0.149.0 | MIT | **Test-only**: loads `docs/openapi.yaml` (OpenAPI 3.1) and validates every request/response pair of the `internal/httpapi` tests (`contract_test.go`); never linked into the server binary |
| `github.com/brutella/dnssd` | v1.2.14 | MIT | mDNS / DNS-SD: the server advertises `_bubbles._tcp` and `<mdns-name>.local` (`internal/discovery`); the Go SDK browses for servers (`pkg/bubblesclient`). Pure Go, so it cross-compiles with `CGO_ENABLED=0`; also pulls in `github.com/miekg/dns` v1.1.61 (BSD-3-Clause) and, on Linux, `github.com/vishvananda/netlink` (Apache-2.0) |
| `github.com/modelcontextprotocol/go-sdk` | v1.8.0 | MIT / Apache-2.0 (license transition) | MCP server and client: the eight tools of `internal/mcpserver`, Streamable HTTP at `/api/v1/mcp` and the stdio bridge `bubbles mcp`; same version as netdiag. Pulls in indirect `github.com/google/jsonschema-go` (MIT, tool schemas), `github.com/segmentio/encoding` + `asm` (MIT), `github.com/yosida95/uritemplate/v3` (BSD-3-Clause), `golang.org/x/oauth2` (BSD-3-Clause) and `github.com/golang-jwt/jwt/v5` (MIT) |

## Tray app (`tray/`, separate Go module, CGO allowed)

The tray is its own module (`tray/go.mod`, `replace github.com/donaldsteele/bubbles => ../`). The server never imports it and its builds stay `CGO_ENABLED=0`.

| Module / package | Version | License | Purpose |
|------------------|---------|---------|---------|
| `github.com/wailsapp/wails/v3` | v3.0.0-beta.28 | MIT | Desktop shell (tray icon, windows, WebView2 / WebKit), bundled notifications service (`pkg/services/notifications`) and the `wails3` CLI. The newest v3 tag on 2026-10-11 (every v3 release is a pre-release); CI installs the CLI at the version recorded in `tray/go.mod`. `github.com/gen2brain/beeep` is not needed: the pinned version ships the notifications service for Windows, macOS and Linux |
| `github.com/zalando/go-keyring` | v0.2.8 | MIT | Device token in the OS keychain (Credential Manager, Keychain, Secret Service) |
| `golang.org/x/sys` | v0.49.0 | BSD-3-Clause | `windows/registry` for the HKCU Run key (start at login) |
| `@wailsio/runtime` (npm, `tray/frontend`) | 3.0.0-beta.28 | MIT | Wails JS runtime and generated bindings used by the sign-in window |
| `vite` (npm, dev) | 8.3.4 | MIT | Bundles the sign-in window |
| `typescript` (npm, dev) | 6.0.3 | Apache-2.0 | Type checks the sign-in window |

The tray also uses `pkg/bubblesclient`, `pkg/notifyrules` and `pkg/protocol` of the main module. On Linux it builds with `-tags gtk3` (`libgtk-3-dev`, `libwebkit2gtk-4.1-dev`).

## npm (web app, `web/`)

Pinned with `save-exact=true` (see `web/.npmrc`). Build and test tooling only; the
shipped bundle contains the runtime code of `svelte` and nothing else from this list.

| Package | Version | Kind | License | Purpose |
|---------|---------|------|---------|---------|
| `svelte` | 5.57.2 | dev (bundled) | MIT | UI component framework (Svelte 5 runes) |
| `vite` | 8.3.4 | dev | MIT | Dev server and production bundler; writes `internal/webui/dist` |
| `@sveltejs/vite-plugin-svelte` | 7.3.1 | dev | MIT | Compiles `.svelte` files for Vite |
| `typescript` | 6.0.3 | dev | Apache-2.0 | Type checking (kept below 6.1 for typescript-eslint) |
| `@types/node` | 26.6.5 | dev | MIT | Node type definitions for `vite.config.ts` |
| `svelte-check` | 4.7.6 | dev | MIT | Svelte and TypeScript diagnostics (`npm run check`) |
| `eslint` | 10.12.0 | dev | MIT | Linter (flat config) |
| `@eslint/js` | 10.0.1 | dev | MIT | ESLint core recommended rules |
| `eslint-plugin-svelte` | 3.23.1 | dev | MIT | Svelte lint rules |
| `typescript-eslint` | 8.71.1 | dev | MIT | TypeScript parser and lint rules |
| `globals` | 17.13.0 | dev | MIT | Browser and Node global variable sets for ESLint |
| `prettier` | 3.9.10 | dev | MIT | Code formatter (`prettier --check`) |
| `prettier-plugin-svelte` | 4.1.1 | dev | MIT | Formats `.svelte` files |
| `vitest` | 5.0.3 | dev | MIT | Unit test runner (`npm run test`) |
| `@testing-library/svelte` | 5.4.2 | dev | MIT | Renders Svelte components in unit tests |
| `jsdom` | 30.1.2 | dev | MIT | DOM environment for unit tests |
| `@playwright/test` | 1.64.0 | dev | Apache-2.0 | End-to-end test runner (`npm run test:e2e`) |
| `@axe-core/playwright` | 4.13.0 | dev | MPL-2.0 | Accessibility scans in the end-to-end suite (pulls in `axe-core` 4.13.0, MPL-2.0) |
| `openapi-typescript` | 7.13.0 | dev | MIT | Generates `src/lib/api/schema.d.ts` from `docs/openapi.yaml` (peer range ^5 of typescript is overridden by `legacy-peer-deps` in `web/.npmrc`) |
| `openapi-fetch` | 0.17.0 | runtime (bundled) | MIT | Typed fetch client over the generated schema |
| `markdown-it` | 15.0.2 | runtime (bundled) | MIT | Markdown parser (html disabled, images disabled) |
| `dompurify` | 3.4.16 | runtime (bundled) | Apache-2.0 OR MPL-2.0 | Sanitizes rendered Markdown HTML |
| `uqr` | 0.1.3 | runtime (bundled, lazy chunk) | MIT | Renders QR codes as SVG in the browser (invite, setup and connection-string links never leave the page) |
| `@types/markdown-it` | 14.2.0 | dev | MIT | Types for markdown-it |
| `emojibase-data` | 17.0.0 | dev (build-time data) | MIT | Emoji, shortcodes and skin tones; `web/scripts/copy-emoji.mjs` writes `/emoji/data.json`, loaded lazily by the in-app picker (no CDN) |
| `@lucide/svelte` | 1.55.0 | runtime (bundled) | ISC | Lucide icon components |
| `vite-plugin-pwa` | 2.0.0 | dev | MIT | Builds the service worker (injectManifest, `src/sw.ts` to `sw.js`) and `manifest.webmanifest`; registration is manual (`injectRegister: false`) to satisfy the CSP |
| `workbox-build` | 7.4.1 | dev | MIT | Peer of vite-plugin-pwa: injects the precache manifest into the worker |
| `workbox-window` | 7.4.1 | dev | MIT | Peer of vite-plugin-pwa (not bundled; registration uses the native API) |
| workbox-precaching / workbox-routing / workbox-strategies | 7.4.1 | dev (bundled into sw.js) | MIT | App-shell precache, route matching and CacheFirst / NetworkOnly strategies in the service worker |
| @resvg/resvg-js | 2.6.2 | dev | MPL-2.0 | `web/scripts/gen-icons.mjs` renders `src/assets/logo.svg` to the committed PNG icons in `web/public/icons/` |

## Web fonts

`web/src/assets/fonts/` holds Latin-subset woff2 files copied from netdiag (`web/vendor/fonts`): Inter Variable (`@fontsource-variable/inter` 5.3.0) and JetBrains Mono 400/700 (`@fontsource/jetbrains-mono` 5.3.0), both SIL OFL 1.1 (`OFL-Inter.txt`, `OFL-JetBrainsMono.txt` alongside). Served from the same origin; no CDN.
