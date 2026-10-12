# Contributing to bubbles

Thanks for helping. bubbles is a single Go binary with an embedded Svelte web
app. Before you start, read [docs/security.md](docs/security.md) and
[SECURITY.md](SECURITY.md) so you know which guarantees matter.

## Development setup

You need:

- Go 1.26.9 or newer (CI pins 1.26.9). If you manage Go yourself, set
  `GOTOOLCHAIN=local` so the go command uses your install instead of
  downloading a toolchain.
- Node 26 and npm 11
- Git
- Docker (optional, for the container image)

Clone the repository, then:

```sh
npm --prefix web ci                 # web dependencies (exact lockfile)
npm --prefix web run build          # writes internal/webui/dist (embedded in the server)
go build ./cmd/bubbles
```

For iterating on the UI, run `npm --prefix web run dev` (Vite dev server) next to
a running server.

## Checks before you push

```sh
bash scripts/gofmt-check.sh         # must print nothing
go vet ./...
go test ./...
npm --prefix web run check          # svelte-check, eslint, prettier
npm --prefix web run test           # Vitest
```

CI runs the same checks on Linux, Windows and macOS, plus a smoke test of
`bubbles serve` and a cross-build of all six server targets.

Race tests need cgo on the host:

```sh
CGO_ENABLED=1 go test -race ./...
```

## Conventions

- **The server stays pure Go.** Every server build uses `CGO_ENABLED=0`. Only the
  optional tray module (`tray/`) may use cgo.
- **One mutation path.** Writes go through the domain services and the single
  store writer, in one transaction that also writes the event row. Do not write
  SQL from handlers.
- **Wire types live in `pkg/protocol`.** The REST contract is
  `docs/openapi.yaml`; regenerate the TypeScript types after changing it.
- **Never log** message bodies, file names, passwords, tokens or push endpoints.
- **Dependencies are pinned.** Use exact versions (no `^` or `latest`) and add a
  row to `docs/DEPENDENCIES.md` with name, version, license and purpose.
- **Tests are deterministic.** Use fake clocks where time matters, loopback only
  for network, and gate live or OS-specific tests behind `BUBBLES_LIVE_*`
  environment variables.
- **No placeholder markers** (TODO or FIXME) in committed code. Open an issue
  instead.
- **Docs follow the code.** If you change a flag, environment variable, setting
  or CLI command, update [README.md](README.md) and the matching file in `docs/`.

## Module path

The Go module path is `github.com/donaldsteele/bubbles`. A fork that publishes
under its own path runs `scripts/set-module-path.ps1 -Module github.com/<owner>/bubbles`
once. Release tags are refused while `go.mod` holds the old `your-org` placeholder.

## Submitting changes

1. Open an issue first for larger changes, so the design can be agreed.
2. Keep each pull request focused, and describe what it changes and how you
   tested it.
3. Include tests for behaviour changes and screenshots for visible UI changes
   (see `web/tests/e2e/screenshots.spec.ts`).
4. Security-relevant changes need a note on the threat they address; report
   vulnerabilities privately as described in [SECURITY.md](SECURITY.md).

By contributing you agree that your contribution is licensed under the
Apache License 2.0 (see [LICENSE](LICENSE) and [NOTICE](NOTICE)).

## Windows notes

- Many packages start full HTTPS servers in their tests. On Windows, running the
  whole race suite with default package parallelism can cause loopback connect
  timeouts under load. Use `CGO_ENABLED=1 go test -race -p 4 ./...`.
- The Playwright suite runs one worker locally (`workers: 1` unless `CI` is
  set) because WebKit and Firefox time out when several browsers and servers
  run at once on one machine. One retry is enabled; a test that passes only on
  retry is reported as "flaky" and should be investigated, not ignored.
