# Security checklist (executed)

Executed on 2026-10-12 against a server built from this tree (`CGO_ENABLED=0 go build ./cmd/bubbles`,
Go 1.26.9, Windows 11, local CA, `--no-mdns`). "Live" evidence comes from `curl` against a real
server started on `127.0.0.1:18443` with a throwaway data directory; "test" evidence is the named Go
test run with `go test -count=1`. Secrets in the transcripts below are replaced by placeholders.

Setup used for every live check:

```sh
bubbles serve --data-dir ./data --https-addr 127.0.0.1:18443 --http-addr 127.0.0.1:18080 --no-mdns
C="curl -s --ssl-no-revoke --cacert data/tls/ca.crt"      # TLS verified against the server's own CA
```

Result: the last run passed 26 of 26 items on the previous build. The first-run items were replaced by the first-start admin password, sign-up window and tray CA items below, which are **NOT RUN** yet. Re-run the whole list on the release build; re-run it after any change to headers, auth, files, backup or reset.

## 1. Transport and headers

- [x] **PASS: security headers and the exact CSP on SPA responses.**
  `$C -D - -o /dev/null https://127.0.0.1:18443/` returned `200` with
  `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' blob: data:; media-src 'self' blob:; connect-src 'self'; worker-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`
  (identical to the locked decision). Test: `go test ./internal/httpapi -run 'TestSecurityHeaders|TestCSPOnSPAResponses'` -> `ok`
  (asserts `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `Cross-Origin-Opener-Policy: same-origin`,
  the minimal `Permissions-Policy`).
- [x] **PASS: the same headers and CSP on API and error responses.**
  `$C -D - -o /dev/null https://127.0.0.1:18443/api/v1/status` and `.../api/v1/nope` (404) both carried the CSP and
  `X-Content-Type-Options: nosniff`; API answers carry `Cache-Control: no-store`.
- [x] **PASS: the plain HTTP listener serves only onboarding assets and sets no cookies.**
  `curl -sD - http://127.0.0.1:18080/api/v1/status` -> `308`, `Location: https://127.0.0.1:18443/api/v1/status`;
  `curl -sD - http://127.0.0.1:18080/` -> `200` with its own CSP (`default-src 'none'; style-src 'self'; ...`), no `Set-Cookie`.
  Test: `go test ./internal/onboarding -run TestNoCookiesAnywhere`.

## 2. Sessions, cookies, Origin

- [x] **PASS: cookie flags.** Live setup response:
  `Set-Cookie: __Host-bubbles_session=<redacted>; Path=/; Max-Age=7776000; HttpOnly; Secure; SameSite=Strict`
  (no `Domain`, so the `__Host-` prefix is honoured; 90-day absolute lifetime). Tests:
  `go test ./internal/auth -run TestSessionCookieAttributes` and
  `go test ./internal/httpapi -run TestSessionCookieAttributesAndHashOnlyStorage` -> `ok`.
- [x] **PASS: Origin check on state-changing cookie routes.** Live, `POST /api/v1/channels` with a valid session cookie:
  `Origin: https://evil.example` -> **403**; no `Origin` -> **403**; `Origin: https://127.0.0.1:18443` -> **201**.
  Test: `go test ./internal/httpapi -run 'TestCrossOriginPostsAreForbidden|TestSameOrigin'` -> `ok`.
- [x] **PASS: Origin check on the WebSocket upgrade.** Live, `GET /api/v1/ws` with `Upgrade: websocket` and a valid cookie:
  `Origin: https://evil.example` -> **403**; no `Origin` -> **403**; right `Origin` -> **101**; no credentials -> **401**.
  Test: `go test ./internal/wsgateway -run TestUpgradeRefusals` -> `ok`.
- [x] **PASS: the LAN guard refuses a non-private source with 403.**
  A real non-private source cannot be produced from one host, so this is test evidence:
  `go test ./internal/netutil -run 'TestIsPrivate|TestGuardFuncReadsAllowListPerRequest'` and
  `go test ./internal/httpapi -run 'TestNetworkGateCoversAuthRoutes|TestPairingHTTPNetworkGateAndValidation'` -> `ok`
  (private, loopback, CGNAT, link-local and ULA sources pass; public sources get `403 forbidden` on every route, including setup, login and pairing; the allow-list is read per request).
  The server also refuses to start on a public bind without `--allow-public`.
- [ ] **NOT RUN: generated admin password at first start.** Expected: the console banner shows `Sign in as: admin` and
  the password; `<data-dir>/admin-password.txt` exists and is owner-only; after the admin changes the password the file is
  gone; on a later start, while the file exists, the banner says "The first admin password is in <path>".
  Check on a fresh data directory with `ls -l` (Unix) or `icacls` (Windows).
- [ ] **NOT RUN: sign-up window.** Expected: `GET /api/v1/status` returns `registration_open` and `registration_closes_at`;
  `POST /api/v1/admin/registration` with `{"action":"close"}` makes `POST /api/v1/register` refuse; `{"action":"reopen"}`
  opens it again; the closing time survives a restart; sign-up is limited to 20 attempts per IP address per hour.
- [ ] **NOT RUN: the tray refuses an unconstrained CA.** Expected: a CA without critical name constraints limited to local
  names and private IP ranges is refused, and nothing is installed.
- [x] **PASS: login lockout holds under concurrency.** Live, 30 simultaneous wrong-password logins for one user:
  **5 x 401, 25 x 429** (exactly `LoginUserMaxFailures` password checks reached argon2); the correct password during the lockout -> **429**.
  Tests: `go test ./internal/auth -run 'TestLoginBurstReachesPasswordCheckAtMostLimit|TestLoginFailuresAndLockout'`,
  `go test ./internal/httpapi -run 'TestLoginLockoutAfterFiveFailures|TestLoginLockoutPerIP|TestDeviceLoginShareLockoutWithLogin'` -> `ok`.

## 3. Secrets at rest

- [x] **PASS: tokens are stored only as hashes.** After creating a session, an invite, an app token, a webhook and a password
  reset on the live server, every column whose name contains `hash`, `token`, `secret`, `code` or `ciphertext` was listed
  with SQLite (`modernc.org/sqlite`, read-only) and the whole database was searched for the plaintext values:

  ```text
  table=users           column=pw_hash        typeof=text length=97   (argon2id PHC string)
  table=sessions        column=id_hash        typeof=blob length=32   (SHA-256)
  table=invites         column=code_hash      typeof=blob length=32
  table=password_resets column=code_hash      typeof=blob length=32
  table=app_tokens      column=token_hash     typeof=blob length=32
  table=webhooks        column=token_hash     typeof=blob length=32
  needles searched=8 plaintext hits=3   (all 3 hits are the message-body control canary in messages/events; 0 for session cookie, invite code, app token, webhook token, reset code, passwords)
  ```

  `device_tokens.token_hash` and `pairing_requests.poll_secret_hash` are `BLOB NOT NULL` in the schema
  (`internal/store/migrations`) and covered by `go test ./internal/auth -run TestDeviceLoginIssuesHashedScopedToken` and
  `go test ./internal/apps -run 'TestAppsIssueTokenStoresOnlyHash|TestPairingLimits'` -> `ok`.
  Reproduce the search with any SQLite client: `SELECT count(*) FROM <table> WHERE instr(CAST(<col> AS TEXT), '<secret>') > 0`.
- [x] **PASS: logs contain no bodies, tokens or passwords.** The server was started with stderr captured (`server.log`) next to
  the rotating log file `data/logs/bubbles.log`. During the run the admin posted the message `CANARY-BODY-7d41e0 hello`, signed in
  with the known password `Pw-Canary-8f3a91c2-XyZ`, created a second account (`Pw-Second-...`), uploaded `canary-filename-5521.txt`,
  and issued a session, an app token (`bub_...`) and a webhook token. `grep -rlF` over both log files for the body canary, both passwords,
  the file-name canary, the first 8 characters of the session cookie, of the app token and of the webhook token returned **0 files each**.
  One expected exception: the generated admin password is printed on the console banner (stderr) at first start so the operator can sign in; it is
  never written to `data/logs/bubbles.log`. Tests: `go test ./internal/httpapi -run 'TestWebhookDoesNotLogOrAuditBodies|TestWebhookTokenAndBodyNeverReachTheLog'` -> `ok`.
- [x] **PASS: backup encryption is the default.** `bubbles backup --out nopass.bbk` without a terminal or passphrase file refuses:
  `no terminal to ask for a passphrase: pass --passphrase-file, or --no-encrypt to accept an unprotected archive`.
  With `--passphrase-file` the archive starts with `age-encryption.org/v1` and contains neither `BEGIN` (the CA key) nor the message canary.
  Tests: `go test ./internal/backup -run TestRoundTripEncrypted`, `go test ./cmd/bubbles -run 'TestBackupRestoreCLI|TestBackupNoEncryptWarns'`,
  `go test ./internal/httpapi -run TestAdminBackup` -> `ok`.
- [x] **PASS: CA key and data directory permissions.** On Windows, `icacls data/tls/ca.key` lists only
  `NT AUTHORITY\SYSTEM:(I)(F)`, `BUILTIN\Administrators:(I)(F)` and the owner `(I)(F)` (protected DACL inherited from the data directory).
  On Unix the key files are 0600 and directories 0700. Tests: `go test ./internal/tlsca -run TestKeyFilesAreOwnerOnly`,
  `go test ./internal/config -run TestEnsureDataDirAppliesProtectedDACL`, `go test ./cmd/bubbles -run TestKeyFilesAreOwnerOnly` -> `ok`.

## 4. Rate limits

- [x] **PASS: messages (10/s, burst 20 per user).** Live, 80 concurrent posts by one user: **62 x 201, 18 x 429**.
  Test: `go test ./internal/httpapi -run TestChatMessageRateLimitPerUser` -> `ok`.
- [x] **PASS: alerts (60/min, burst 20 per app).** `go test ./internal/httpapi -run TestAlertsHTTPRateLimitIs60PerMinuteBurst20` -> `ok`.
- [x] **PASS: incoming webhooks (30/min, burst 10).** `go test ./internal/httpapi -run TestWebhookRateLimitIs30PerMinuteBurst10` -> `ok`.
- [x] **PASS: pairing (5/min per IP create, 2/s poll, 20 pending).** `go test ./internal/httpapi -run TestPairingHTTPLimits`,
  `go test ./internal/apps -run 'TestPairingLimits|TestPairingPollIsRateLimitedPerIP'` -> `ok`.
- [x] **PASS: WebSocket client frames.** `go test ./internal/wsgateway -run 'TestClientFrameRateLimitCloses1008|TestParseClientFrame'` -> `ok`
  (flooding closes with 1008; frames over 4096 bytes close with 1009).
- [x] **PASS: API per session, per app token.** `go test ./internal/httpapi -run 'TestSessionAPIRateLimit'`,
  `go test ./internal/apps -run TestAppsTokenLimiterIs10PerSecondBurst30` -> `ok`.

## 5. Files, apps, push

- [x] **PASS: file serving headers.** Live, after uploading a PNG, a JPEG with GPS, an SVG containing `<script>` and a text file:
  all four answers carry `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox; default-src 'none'; img-src 'self'; media-src 'self'`
  and `Cache-Control: private, max-age=31536000, immutable`. PNG and JPEG are `Content-Disposition: inline`; the SVG and the text file are
  `Content-Disposition: attachment` with `Content-Type: application/octet-stream`. An unauthenticated fetch of a file -> **401**.
  Test: `go test ./internal/httpapi -run TestFilesSVGIsServedAsAnAttachmentWithSandboxCSP` -> `ok`.
- [x] **PASS: app-token scopes and channel allowlists.** Live, with a token holding only `messages:write`: `GET /api/v1/admin/users` -> **403**,
  `GET /api/v1/channels` -> **403**, `GET /api/v1/channels/{id}/messages` -> **404** (no read scope), `POST /api/v1/admin/reset` -> **403**.
  Tests: `go test ./internal/httpapi -run 'TestAppsScopesAndChannelAllowlistAreEnforced|TestAppsBearerIsRefusedOnCookieOnlyRoutes'` -> `ok`.
- [x] **PASS: MCP scopes.** `go test ./internal/mcpserver -run 'TestMCPScopeRules|TestMCPMissingScopeIsAToolError|TestMCPReadMessagesIsLimitedToTheBotsChannels'` -> `ok`
  (a tool without its scope is a tool error; reads are limited to the bot's channels).
- [x] **PASS: device-token allowlist.** `go test ./internal/httpapi -run 'TestDeviceTokenReadsOnly|TestDeviceLoginShareLockoutWithLogin'` -> `ok`
  (a tray device token carries only `events:read`, `messages:read` and `notify`; it cannot post, upload, manage apps or call admin routes;
  it dies with a password change or deactivation).
- [x] **PASS: push SSRF guard.** `go test ./internal/push -run 'TestPushBlockedAddr|TestPushDialerBlocksPublicNameResolvingToLoopback|TestPushClientDoesNotUseProxiesAndStopsAtRedirects'` -> `ok`
  (private, loopback, link-local/metadata, CGNAT, documentation and transition ranges are refused; the validated IP is the one dialled;
  a public name that resolves to a private address is refused; no proxies, redirects not followed).

## 6. Reset and tray

- [x] **PASS: reset endpoint gating.** Live: `POST /api/v1/admin/reset` unauthenticated -> **401**; as a member -> **403**;
  as an app bearer -> **403**; as admin with a wrong password -> **403** `the password is incorrect`; with a wrong instance name -> **403**
  `the confirmation does not match the instance name`. Tests: `go test ./internal/httpapi -run TestAdminReset` (7 tests, including the
  fourth attempt in an hour -> 429 and the `web reset disabled` setting) and
  `go test ./internal/app -run 'TestResetDeniedAttemptsAreAuditedAndWebResetCanBeDisabled|TestMaintenanceModeRefusesEverythingButHealthz'` -> `ok`.
- [x] **PASS: factory reset leaves no canary.** `bash scripts/smoke-serve.sh --reset-roundtrip` ->
  `smoke-serve: reset round trip OK` / `smoke-serve: OK` (posts a canary message and photo, factory resets over the API, checks that the
  a new admin password is shown and that neither canary survives in any file under the data directory). Test:
  `go test ./internal/app -run TestResetFactoryOverHTTP` -> `ok`.
- [x] **PASS: the Wails tray origin and window gate.** `cd tray && go test -count=1 . -run 'TestWindowAllowed|TestOriginAllowed|TestOriginGateMiddleware'` -> `ok`
  (only the configured server origin can be loaded; other windows and origins are refused).

## 7. Supply chain

- [x] **PASS (documented): `govulncheck ./...` on Go 1.26.9.** The 18 standard-library findings (net/http, crypto/tls, html/template, encoding/asn1,
  mime/multipart, net/url, os, encoding/xml, dnsmessage) were all fixed in Go 1.26.6 to 1.26.9; `go.mod` now pins `toolchain go1.26.9`
  and the Docker build image is `golang:1.26.9-alpine`. One finding remains by design and is not reachable: `GO-2026-5932`
  (`golang.org/x/crypto/openpgp` is unmaintained, no fixed version exists). bubbles does not import `openpgp`; govulncheck lists it as
  "a vulnerability in a module you require, but your code doesn't appear to call it", and it does not fail the exit code. Same result in `tray/`.
- [x] **PASS: `npm --prefix web audit --omit=dev --audit-level=high`** -> `found 0 vulnerabilities`.
- [x] **PASS: load and robustness.** `go run ./tools/loadtest --clients 200 --senders 20 --rate 5 --duration 60s --assert-p95 150ms --assert-rss 300MB`
  -> sent 6001, received 60010 of 60010 expected, lost 0, duplicates 0, p95 2.1 ms, server RSS peak 122 MiB. All nine fuzz targets ran 120 s with no crash.
