# Security

This document states what bubbles protects, against whom, and where its
guarantees end. Read it before you run bubbles for anyone else.

To report a vulnerability, follow [SECURITY.md](../SECURITY.md). Do not open a
public issue for it.

## Threat model

bubbles is for a trusted local network: a home, an office or an event site.
It is not designed for the internet.

| Actor | Protected? | How |
|---|---|---|
| **LAN attacker** (someone on the same Wi-Fi) | Partly | All app traffic is HTTPS with a local CA. Source-IP guard refuses non-private clients. Strict CSP, Origin checks and secure cookies. Sign-up is open to anyone on the LAN until it closes, and passwords have no strength rule, so login and sign-up are rate limited, the admin password is generated, and the admin should change it and close sign-up on shared networks. |
| **Malicious app token** (a leaked or misused integration token) | Yes, limited | Scoped tokens, optional channel allowlist, per-token rate limits, revocation, `last_used_at` and audit entries. A token acts only through its scoped API, and never reaches the reset endpoint. |
| **Lost or stolen device** | Partly | An admin can end a user's web sessions ("Sign out everywhere"); deactivating the user also revokes their sessions and tray device tokens, so they lose access at once. A single tray device is revoked by its user (signing out in the tray, or the `DELETE /api/v1/me/devices/{id}` API). Anything already on the device (cached messages, downloaded files) stays there until you clear it. Use the device's own lock and encryption. |
| **Compromised server host** (malware, a root user, someone with physical access to the running machine) | **No** | Out of scope. The host holds the CA key, the database and the files in plaintext. |
| **Curious or malicious admin** | **No** | Admins can read data through the database; see the next section. |
| **End-to-end privacy from the server** | **No** | There is no end-to-end encryption. The server is trusted with message content. |

## What admins can and cannot see

Admin role does not make an admin a member of private channels or DMs, so the
chat UI does not show them to admins. The event feed sent to a user is computed
for that user only.

However, the server stores messages in an SQLite database, and anyone with
read access to the data directory (including the administrator on the host,
and anyone who can copy the files) can read everything, including private
channels, DMs, file contents and password hashes. bubbles does not encrypt
the database at rest. Protection at rest comes from:

- restrictive file permissions (Unix: directories 0700, files 0600; Windows: a
  protected DACL that grants access only to SYSTEM, Administrators and the
  owner),
- full-disk encryption provided by the operating system (recommended),
- encrypted backups by default.

Treat the server as a trusted machine that the chat members trust.

## Transport and certificates

- **HTTPS everywhere.** The server listens for HTTPS (default port 28225). The
  plain-HTTP listener (port 28226) serves only the onboarding page and CA
  downloads; any other path redirects to HTTPS. No cookies, authentication or
  API run on the HTTP listener. TLS 1.2 is the minimum.
- **Local CA.** bubbles creates an ECDSA P-256 CA with a 10-year validity, and
  issues a 397-day leaf certificate for the server's current LAN addresses and
  names. The leaf is renewed before expiry and when addresses change.
- **Name constraints (defence in depth).** The CA certificate is marked critical
  with name constraints: DNS names `local`, `home.arpa`, `lan`, `internal`,
  `localdomain`, `localhost`, the server hostname and any `--tls-extra-dns`
  names given at creation; IP addresses only in the private, loopback and
  link-local ranges and CGNAT (`100.64.0.0/10`) used by Tailscale. These limit
  what the CA may sign **if** a client enforces them. Not every platform or
  browser enforces name constraints on user-installed roots, so do not rely on
  them alone.
- **Tray refuses unconstrained CAs.** The desktop tray app installs a CA only if
  its critical name constraints limit it to local names and private IP ranges. A
  fake `bubbles` server on the network therefore cannot get a certificate trusted
  for public websites through the tray.
- **The CA key is the root of trust.** Anyone with the CA private key can make a
  certificate for any name the constraints allow, and then intercept traffic
  from devices that trust the CA. The key is stored with 0600 permissions (or a
  protected DACL on Windows) and never leaves the host except inside an
  encrypted backup. A backup made with `--no-encrypt` contains the CA key in
  plaintext.
- **Fingerprint verification.** Devices should compare the CA SHA-256 fingerprint
  with the one on the server console or admin overview before trusting it. This
  is the check that protects you from a rogue CA installed by someone else. The
  tray app shows the same fingerprint at its confirm step.
- **Bring your own certificate.** `--tls-cert` and `--tls-key` disable local
  issuance and use your own PKI.

## Authentication

- **Passwords:** argon2id (time 3, memory 64 MiB, 4 threads, 16-byte salt,
  32-byte key). Any non-empty password up to 128 characters is accepted,
  including weak ones such as `password1`. There is no common-password list and no
  strength rule. bubbles is meant for trusted LANs and relies on the login rate
  limits below. At most four hashes run at once.
- **Open sign-up.** Sign-up is open by default. Anyone on a private or allowed
  network can create an account at `/register` with a name and a password, without
  an invitation. It closes by itself after `registration_window_hours` (default 24)
  and stays closed across restarts. An admin can reopen it or close it at once.
  Sign-up attempts are limited to 20 per IP address per hour. This is the intended
  model for a trusted home or office LAN. On a shared network, close sign-up and
  add people with invitations or **Add person**.
- **Admin password.** The first `admin` password is generated at first start (16
  characters in four groups, for example `k7mq-x4pz-9vbd-h2nc`). It is printed on
  the console and in `docker logs`, and saved in `<data-dir>/admin-password.txt`,
  which is owner-only (0600 on Unix; on Windows the data directory's protected DACL
  applies). The file is deleted when the admin changes the password. Anyone who can
  read the console output or the data directory can read it, so change it at once.
  A factory reset creates a new one.
- **Login errors** are generic. Failed logins are counted in a sliding 15-minute
  window: 5 failures per account and 20 per IP address, then further attempts are
  rejected.
- **Changing a password** revokes every other session and every device token.
- **Sessions:** a 256-bit random token in a cookie named `__Host-bubbles_session`
  with `Secure`, `HttpOnly`, `SameSite=Strict` and `Path=/`. Only its SHA-256 hash
  is stored. Sessions expire after `session_idle_days` (default 30, maximum 90)
  and in any case 90 days after sign-in.
- **App and device tokens:** `bub_` followed by 32 random bytes in base32. Only
  the SHA-256 hash is stored. The full secret is shown once; the first 8
  characters identify the token. Tokens have scopes, optional expiry and optional
  channel allowlists.
- **Desktop tray (device tokens):** the tray signs in with `POST /auth/device-login`, which
  uses the same lockout counters as the web login. The device token carries only
  `events:read`, `messages:read` and `notify`; it may open the WebSocket, read the bootstrap
  snapshot, messages and threads, ask for a handoff code and revoke itself. Every other
  route (admin, `/me`, posting, uploads, apps) answers `403`. To open the web app the tray
  loads `/auth/handoff?code=…`: a 256-bit one-time code, valid for 60 seconds, held only as
  a SHA-256 hash in memory, bound to the device's user and consumed on first use. It
  creates a normal web session. The tray pins the CA fingerprint the user confirmed and
  never re-trusts a changed fingerprint without a new confirmation; the token lives in the
  OS keychain.
- **First administrator:** created at first start with a generated password, as
  described above.
- **Deactivation** revokes sessions and tokens immediately and closes live
  connections.

## Request protection

- **Source-IP guard.** Requests are accepted only from private, loopback, CGNAT
  and link-local addresses, plus any networks listed in the `allowed_cidrs`
  setting. Other sources get 403. A server with only public addresses refuses to
  start unless you pass `--allow-public`.
- **Origin checks.** WebSocket upgrades and state-changing cookie-authenticated
  requests must come from the server's own origin.
- **Content Security Policy** on every HTTPS response:
  `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' blob: data:; media-src 'self' blob:; connect-src 'self'; worker-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`.
  The web app loads nothing from other origins: fonts, emoji data and icons are
  bundled.
- Other headers: `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`,
  a minimal `Permissions-Policy` (camera, microphone, geolocation, payment and USB
  all disabled), and `Cross-Origin-Opener-Policy: same-origin`.
- **API responses** for authentication, errors, reset, webhooks and MCP are sent
  with `Cache-Control: no-store`.

## Rate limits

| Scope | Limit |
|---|---|
| App token requests | 10 per second, burst 30, per token |
| Authenticated API | 20 per second, burst 50 |
| Posting messages | 10 per second, burst 20, per user |
| Alert ingest | 60 per minute, burst 20, per app |
| Incoming webhook | 30 per minute, burst 10 |
| Pairing requests | 5 per minute per IP |
| Pairing polls | 2 per second, burst 20, per IP |
| Login failures | 5 per account and 20 per IP address, sliding 15-minute window |
| Sign-up | 20 per IP address per hour |

Limiter state is capped at 10,000 keys.

## Uploads and file serving

- Uploads are stored under names derived from their SHA-256 hash. The user's
  file name never touches the filesystem.
- Image metadata, including GPS location in EXIF, is removed from JPEG, PNG and
  WebP files. If removal is not possible (for example a HEIC photo), the file is
  stored as a plain file, and the UI warns that metadata was not removed.
- Images above 50 megapixels are stored without a thumbnail and served as
  downloads.
- Only a list of safe image, video and audio types is shown inline. Everything
  else, including SVG, HTML and PDF, is served as a download.
- File responses carry `X-Content-Type-Options: nosniff`, a sandbox CSP
  (`sandbox; default-src 'none'; img-src 'self'; media-src 'self'`) and
  `Cache-Control: private, max-age=31536000, immutable`.
- Quotas: per-file limit (`upload_max_bytes`), per-user limit
  (`user_quota_bytes`), and a disk reserve (`disk_reserve_bytes`) below which
  uploads are refused with 507.

## Rendering and links

Messages are Markdown rendered in the browser with raw HTML disabled, then
sanitised. Links are limited to `http`, `https` and `mailto` and carry
`rel="noopener noreferrer nofollow"`.

## Server-side fetching (SSRF guard)

bubbles does not fetch user-supplied URLs, link previews included. The only
outbound request is Web Push, and that goes through a guard:

- The server resolves the push host itself and refuses if **any** answer is not
  a public address. Private, loopback, link-local, multicast, documentation and
  other reserved ranges are refused, as are NAT64 and 6to4 ranges.
- It connects to the validated IP address directly, and the dialer checks the
  final address again. This blocks cloud metadata endpoints such as
  `169.254.169.254`.
- Dial timeout 10 seconds; TLS 1.2 minimum.

## Web Push privacy

Web Push stays disabled until an admin turns it on, and needs internet access from the server.
When enabled:

- Payloads are encrypted to the device per RFC 8291, so the push service sees
  only ciphertext.
- `push_payload` defaults to `minimal`, which carries no message content. `full`
  adds more of the message to the encrypted payload.
- The push service operator (Apple for Safari and iOS, Google for Chrome and
  Android, Mozilla for Firefox) can see **when** notifications are sent and
  **which endpoint** they go to, and can tie that to your device and network.
  bubbles cannot prevent that.
- Push endpoints are never logged.

## Backups

- Backups are tar archives with entries mode 0600, encrypted with
  [age](https://age-encryption.org/) using a scrypt passphrase recipient, unless
  you pass `--no-encrypt`.
- `--no-encrypt` writes the CA private key in plaintext. Treat such an archive as
  a secret equal to the CA key.
- Restore needs the passphrase and refuses a non-empty data directory unless
  `--force` is given.
- Backup files you keep outside the data directory are your files. Reset does
  not touch them.

## Reset and erasure limits

Reset is deliberately hard to trigger: admin role, password re-entry, typed
instance name, Origin check, three attempts per hour, and an audit entry for
failed attempts. Web reset can be turned off with `web_reset_enabled`; console
reset is always available.

What reset erases:

- **Database:** the SQLite database is opened with `secure_delete` enabled, so
  freed pages are zeroed. The WAL is checkpointed and truncated, and the database
  is vacuumed during clear content.
- **Files:** uploaded files are moved out of the way instantly and deleted in the
  background. Backups, logs and local database snapshots are unlinked.

What reset **cannot** erase:

- **SSD wear-levelling and copy-on-write filesystems** (APFS, Btrfs, ZFS, NTFS
  with snapshots). Deleted blocks may remain recoverable with forensic tools.
- **Filesystem snapshots, Time Machine, Windows File History, cloud sync** and
  other backups outside bubbles.
- **OS caches and swap.**
- **Encrypted backup archives** you made earlier. They are yours to delete.

For a machine that travels or is shared, enable full-disk encryption (BitLocker,
FileVault, LUKS). With full-disk encryption, erased data cannot be recovered
from a stolen disk without the key.

A factory reset that keeps the CA leaves the old CA trusted on devices; a reset
with **Generate new CA** destroys the CA private key, so the old root can no
longer sign anything. See [admin.md](admin.md#reset) for the difference.

## Known limits

- No end-to-end encryption. The server sees message content.
- No protection at rest against someone who controls the running host.
- Name constraints are advisory on some platforms.
- Sign-up is open to anyone on the LAN until it closes, and weak passwords are
  accepted. Close sign-up, or use invitations, on any network you do not fully trust.
- Client isolation on Wi-Fi networks can stop devices from reaching the server
  (and each other); this is a network limit, not a bubbles fault.
- Binaries are not code-signed. Verify the release with `SHA256SUMS`.
