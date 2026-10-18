# Administering bubbles

This guide is for the person who runs the server. It covers the first start,
accounts, integrations, settings, the reset actions, backups, TLS, and running
bubbles as a service.

## First start

On first start the server creates the account `admin` (display name "Admin",
role admin) with a generated password. The console prints it in a banner:

```
  Sign in as:   admin
  Password:     k7mq-x4pz-9vbd-h2nc
  (also saved in <data-dir>/admin-password.txt; deleted when you change the password)
```

1. Start the server (see the [README](../README.md)) and keep the banner.
2. Open the HTTPS address on the console in a browser, sign in as `admin` with
   that password, and change it.
3. Other people join through sign-up or invitations (below).

The banner also goes to `docker logs`. It is printed on the console only and is
never written to the log files.

The password is also saved in `<data-dir>/admin-password.txt`. The file is
owner-only and is deleted when the admin's password changes. On later starts,
while the file still exists, the banner says "The first admin password is in
<path>".

If you forget the password, run `bubbles admin reset-password admin`. It prints a
one-time reset link. A factory reset also creates a new `admin` with a new
generated password (see [Factory reset](#factory-reset)).

## Admin overview

The overview shows the URLs and QR codes for every current server address, the
CA fingerprint, certificate expiry, storage use, and the number of connections.
Compare the fingerprint with the one on devices during onboarding.

While only the admin's own computer has connected, the overview shows a hint
that the Wi-Fi network may use **client isolation**, which stops devices from
reaching each other and the server. If phones or laptops cannot connect, check
that first and try a different network or access point.

## Users and roles

Roles, from most to least powerful:

| Role | Can do |
|---|---|
| `admin` | Everything, including users, settings, reset, audit log, TLS |
| `member` | Chat, channels they belong to, files, apps they own |
| `guest` | Chat only in channels they are invited to |

An admin is not automatically a member of private channels or DMs. Admin
rights do not give access to other people's conversations.

Each row of the users page has these actions:

- **Reset link**: a one-time password reset link and QR code, valid for a
  limited time.
- **Sign out everywhere**: ends every web session of that user. Tray devices
  are not affected by it.
- **Deactivate**: the user is signed out at once and cannot sign in. Their web
  sessions, tray device tokens and the tokens of apps they own are revoked, and
  their live connections are closed. Their messages stay. **Reactivate** restores
  sign-in but does not restore revoked sessions or tokens.

The role column can be changed on the same page. Admins cannot deactivate
themselves.

The console command `bubbles admin reset-password <username>` also starts a
password reset.

## Sign-up

Sign-up is open by default on a new server. (A server upgraded from v0.1.x keeps
invite-only sign-up until an admin opens it in **Admin > Settings > Sign-up**.)
Anyone on the network (a private or LAN address, or
a range in `allowed_cidrs`) can create an account at `/register` with a
**Your name** and a password. The name is shown as typed and must be unique
(case-insensitive). An @handle is made from it automatically: "Ada Lovelace"
becomes @ada-lovelace. People sign in with their name or their handle. New
accounts are members.

Sign-up closes by itself after `registration_window_hours` (default 24). The
choices in the UI are 1 hour, 4 hours, 1 day, 3 days and Never. The window starts
at first start, after a factory reset, and whenever an admin turns sign-up on or
presses **Reopen**. The closing time is stored, so restarting the server does not
reopen sign-up.

Admin > Settings > **Sign-up** shows the state ("Open until …", "Open, no time
limit" or "Closed") and has:

- a switch, **Anyone on the network can create an account** (`open_registration`);
- the select **Close sign-up automatically after** (`registration_window_hours`);
- **Reopen**, which starts a new window, and **Close now**.

Sign-up attempts are limited to 20 per IP address per hour.

After sign-up closes, add people with an invitation link or QR code (below), or
with **Add person** on Admin > Users (name, password and role).

## Invitations

Invitations are links or QR codes that the admin creates. They work whether
sign-up is open or closed; the person who opens one chooses a name and a password.
Each has:

- a **role** (`admin`, `member` or `guest`),
- an **expiry** (default 72 hours; the web form accepts 1 hour to 90 days),
- a **maximum number of uses** (default 1; the web form accepts 1 to 1000).

Console equivalent:

```sh
bubbles admin create-invite --role member --expires 72h --uses 1
```

## Channels

Channel types:

- **public**: any non-guest user can find and join.
- **private**: invite-only.
- **DM** and **group DM** (3 to 9 people): fixed membership.

Each channel can have a retention period (the `default_retention_days` setting
sets the default). Channels can be archived.

## Apps, tokens and pairing

Apps are accounts for integrations such as netdiag or your own scripts.

- An app has one or more **tokens** (`bub_…`). A token is shown once when it is
  created; bubbles stores only its hash and displays the first 8 characters for
  identification.
- Each token has **scopes** and can be limited to a set of channels. A token
  with a channel allowlist never receives events from other channels.
- Tokens can have an expiry (in days, from the new-token form) and can be revoked
  at any time. Last-used time is tracked. A token with no channel selection
  covers all channels its app's bot is a member of.
- **Disable** an app to stop it at once: its live connections are closed and it
  cannot connect until it is enabled again. The app's tokens stay listed.
- **Pairing**: a device or SDK client asks to pair, and an admin approves the
  request after comparing the pairing code, the requested scopes, the source
  address and the CA fingerprint shown on both sides. Approve or deny each
  request. When you approve, tick the channels the bot should join (it is added to
  the ticked channels; archived channels are not offered), and optionally also
  create a channel for the app (`#<app>-alerts`). For an app that asks for
  `alerts:write`, "Also create a channel for this app" starts ticked, unless a
  channel with that name already exists (channel names are unique, archived
  channels included): then tick the existing channel instead. If you grant
  `alerts:write` but tick no channel and leave the new channel unticked, a warning
  next to Approve says the bot will be in no channel and cannot post alerts until
  someone adds it to one; you can still approve. The app learns which channels
  its bot is in when it receives its token. Up to 20 requests can be pending at
  once, and each IP address can create 5 per minute.

See [integrations.md](integrations.md) for the REST API, scopes and rate limits,
and [mcp.md](mcp.md) for the MCP server.

## Webhooks

Incoming webhooks post into one channel. Two formats are accepted:

- the native JSON format (alerts with severity, dedupe key, fields and link),
- a Slack-compatible subset, so tools such as Uptime Kuma, Grafana and Home
  Assistant can post without changes.

Webhook messages always show a "webhook" badge. A webhook can set a display
name, but it cannot impersonate a real user or use Slack mention syntax.

Rate limits: 30 requests per minute with a burst of 10 per webhook.

Treat the webhook URL as a secret. Anyone who has it can post into the channel.
If it leaks, revoke the webhook on the webhooks page and create a new one; the
new URL goes to the tool that posts.

## Settings reference

Settings live in the database and are edited from the admin settings page. The
bootstrap flags (`--data-dir`, `--https-addr`, `--http-addr`, `--mdns-name`,
`--no-mdns`, `--tls-cert`, `--tls-key`, `--tls-extra-dns`, `--allow-public`) are
the only configuration outside the database.

| Key | Default | Meaning |
|---|---|---|
| `instance_name` | `bubbles` | Shown in the UI and the CA name. 1 to 64 characters. |
| `open_registration` | on | Anyone on the network can create an account at `/register`. Turn it off to allow sign-up only through invitations and **Add person**. |
| `registration_window_hours` | 24 | Hours that sign-up stays open after the window starts, from 0 to 8760. 0 means no time limit (the UI's "Never"). The window starts at first start, after a factory reset, and whenever sign-up is turned on or reopened. |
| `open_registration_until` | set by the server | When the current sign-up window ends. Read-only through PATCH; the admin changes it with `POST /api/v1/admin/registration` (`reopen` or `close`). |
| `upload_max_bytes` | 100 MiB | Largest single upload. Maximum 2 GiB. |
| `user_quota_bytes` | 5 GiB | Total upload storage per user. |
| `disk_reserve_bytes` | 1 GiB | Uploads are refused with 507 when free space drops below this. |
| `default_retention_days` | keep forever | Default channel retention, 1 to 3650 days. |
| `web_push_enabled` | off | Enable Web Push (needs internet access from the server). See [security.md](security.md#web-push-privacy). |
| `push_payload` | `minimal` | `minimal` or `full`. Minimal sends no message content. |
| `allowed_cidrs` | empty | Extra source networks allowed beyond private and loopback addresses. Private and loopback addresses are always allowed. Caution: a wrong range can block clients outside private networks, including ones you still need. Check the ranges before saving. |
| `session_idle_days` | 30 | Idle session lifetime, 1 to 90 days. Sessions also end 90 days after sign-in. |
| `alert_renotify_s` | 1800 | Seconds before a still-open, unacknowledged alert notifies again, 10 to 86400. Apps can set their own interval per alert. |
| `mdns_enabled` | on | Advertise `<name>.local` on the LAN. |
| `web_reset_enabled` | on | Allow reset from the web admin page. Console reset always works. |

Every change is written to the audit log.

## Audit log

The audit log records administrative actions, settings changes, reset and backup
events, and token and invitation actions. Entries say whether the actor was a
user or the console (`actor_kind=console`). Console admin commands work on the
database directly, with or without the server running, and are always audited.

Log entries never contain message bodies, file names, passwords, tokens or push
endpoints.

## Command-line administration

```sh
bubbles admin list-users
bubbles admin reset-password <username>
bubbles admin create-invite --role admin --expires 72h --uses 1
```

These commands work whether or not the server is running. Use `bubbles admin -h`
for the full list.

## Backups

Create a backup:

```sh
bubbles backup --out bubbles-2026-10-11.bbk --passphrase-file ./passphrase.txt
```

- Backups are **encrypted by default** with a passphrase (age, scrypt). Keep the
  passphrase somewhere else; without it the backup cannot be restored.
- `--no-encrypt` writes the archive **unencrypted**. It contains the CA private
  key. Do not use it unless the archive is stored somewhere you fully trust.
- Backup files are your files. Reset never touches archives stored outside the
  data directory.

Restore into an empty data directory:

```sh
bubbles restore bubbles-2026-10-11.bbk --passphrase-file ./passphrase.txt
```

Restore refuses a non-empty data directory unless you add `--force`, which
replaces its contents. Stop the server before restoring.

## TLS and certificates

By default bubbles issues its own CA and leaf certificates. The leaf certificate
is renewed when less than 30 days remain or when the set of server addresses
changes (for example after a DHCP change). It is swapped in without a restart.

The admin **Security** page shows the TLS mode, the CA downloads and the leaf
certificate fingerprint, and offers a reissue for the current addresses.

Commands:

```sh
bubbles cert show
bubbles cert reissue --mdns-name bubbles --tls-extra-dns files.lan
bubbles cert export-ca --format mobileconfig --out bubbles-ca.mobileconfig
```

To use your own certificate (your own PKI, or a certificate from a local
authority you already trust), start the server with `--tls-cert` and `--tls-key`.
This disables local issuance, so the local-CA onboarding steps do not apply;
devices must trust your certificate chain. The files are reloaded when they
change.

## Running as a service

```sh
bubbles service install --data-dir <dir>
bubbles service start
bubbles service status
```

- **Windows**: a Windows service with the data directory under
  `%ProgramData%\bubbles`. Installing it adds a firewall rule for the Private
  network profile only.
- **macOS**: a LaunchDaemon. The data directory is
  `/Library/Application Support/bubbles`.
- **Linux**: a systemd unit (see `deploy/systemd/bubbles.service`). The data
  directory is `/var/lib/bubbles`.

`bubbles service uninstall` removes the service.

For Docker and Compose, see the [README](../README.md#docker).

## Desktop tray app

The desktop tray app (`tray/`) keeps a computer signed in to a bubbles server
and opens the chat in its own window. It does not use the admin password after
sign-in.

- **Device sign-in.** The tray asks for the server address and shows the CA
  fingerprint. The user compares it with the one on the admin overview, then
  confirms; the tray can also install the CA into the operating system trust
  store (Windows and macOS show their own prompt; Linux shows the steps to follow).
  The tray refuses a CA that is not limited by critical X.509 name constraints to
  local names and private IP ranges. The user then either creates an account
  (**Your name**, password and a name for the computer) or signs in with a name
  and password, and an optional device name (the computer name by default). The
  server checks the
  credentials with the same lockout as the web sign-in and issues a **device
  token** (`POST /api/v1/auth/device-login`). Each user can hold at most 25
  active device tokens.
- **Where the token lives.** The device token is stored in the operating system
  keychain. It carries only `events:read`, `messages:read` and `notify`. It
  cannot call admin routes, post, upload or manage apps.
- **Handoff to the web app.** To open the chat, the tray asks for a handoff code
  and loads it once in its window. The code is valid for 60 seconds and creates
  an ordinary browser session for the same user.
- **Revoking devices.** Signing out in the tray revokes that device on the server
  when the server can be reached, then deletes the token. A device whose token is
  revoked closes with 4009 and its tray returns to sign-in. A user can list their devices
  (`GET /api/v1/me/devices`, browser session only) and revoke one
  (`DELETE /api/v1/me/devices/{id}`); the web interface has no device list. An admin cannot revoke one device from the
  admin pages, but deactivating a user, or the user changing their password, revokes all
  of their devices.

## Reset

Reset is the most destructive action in bubbles. There are two kinds, and both
can be run from the admin **Danger zone** or from the console.

Requirements for both (web or console):

- admin role (web) and the admin password, re-entered,
- the instance name typed exactly,
- a preview of what will be deleted.

In the web Danger zone each action opens a dialog. It has an optional checkbox,
**Download encrypted backup first**, which asks for a backup passphrase and saves
the backup before anything is deleted. The dialog's submit button is
**Delete content** or **Delete everything**.

Web attempts are limited to three per hour; failed attempts are audited. Set
`web_reset_enabled` to off to disable web reset entirely. The console command
always works.

### Clear content

The Danger zone button **Delete all content…** starts this. It deletes every message, thread reply, reaction, pin, mention, alert, file and
picture, the search index, and the event history. It also resets unread state.

It **keeps** users, channels and memberships, apps and tokens, webhooks,
invites and settings. Users stay signed in. Clients reconnect, see empty
channels, and drop their cached messages.

```sh
bubbles reset --content
```

### Factory reset

The Danger zone button **Delete everything…** starts this. It wipes everything: users, channels, DMs, messages, files, sessions, device and
app tokens, apps, webhooks, invites, pairing requests, push subscriptions, VAPID
keys, the audit log, log files and local database snapshots. The server then
creates a new `admin` account with a new generated password. The password is
shown once on the reset-complete screen and printed on the console. Sign-up opens
again for its window.

Options:

- **Keep the CA (default).** Devices that already trust the CA stay trusted
  after a factory reset.
- **Generate new CA** (`--rotate-ca`; the web dialog's "Generate new CA" box). Destroys the old CA private key and
  issues a new CA. Every device has to install the new CA, and old devices
  still have the old root. Its key is destroyed, so it cannot sign anything
  again, but it stays in the trust store until removed. The per-OS steps for
  removing it are in [onboarding.md](onboarding.md#removing-an-old-ca).
- **Keep server settings** (`--keep-settings`; the default is not to keep them; the web dialog's
  "Keep server settings" box). Carries the settings over. Instance identity and
  secrets are never carried over.

Push subscriptions stop working after a factory reset because VAPID keys are
always regenerated.

```sh
bubbles reset --factory                      # keep CA
bubbles reset --factory --rotate-ca          # new CA
bubbles reset --factory --keep-settings      # carry over settings
```

The console form needs the server stopped and asks you to type the instance
name unless you pass `--yes`. The next start prints the new admin password.

### What reset does not erase

Reset deletes files and the database is opened with `secure_delete` (freed
pages are zeroed and the WAL is truncated). It cannot erase copies outside
bubbles: SSD wear-levelling, filesystem snapshots, OS caches, or backups you made
yourself. Use full-disk encryption on any computer that leaves your control.
See [security.md](security.md#reset-and-erasure-limits).

### Crash safety

Reset writes a journal file (`reset-pending.json`) before its first step. If the
machine loses power mid-reset, the next start completes the reset before any
listener opens.

## Memory

- By default the server sets a 200 MiB soft limit on the Go heap (`debug.SetMemoryLimit`)
  to keep the resident size near 300 MB with 200 connections. The limit is soft:
  a burst of password hashes can exceed it briefly, and the garbage collector
  works harder as the heap nears it.
- Set the `GOMEMLIMIT` environment variable to take over the limit. When it is
  set, the server leaves the Go runtime's setting alone. Examples: `GOMEMLIMIT=512MiB`
  for a larger limit, `GOMEMLIMIT=256MiB` for a smaller one.
- Password hashing is argon2id with 64 MiB of working memory per hash, and at most
  four hashes run at once. After a burst of hashes the server returns memory to the
  OS, at most once every 30 seconds.
- The systemd unit and launchd plist do not set `GOMEMLIMIT`. To set it for a
  service, add an `Environment=GOMEMLIMIT=512MiB` line to the unit (systemd) or an
  `EnvironmentVariables` entry to the plist (launchd), then restart the service.

## Logs and troubleshooting

- The console shows startup errors and addresses. Logs are kept in the data
  directory and are reset by factory reset.
- If the server has public addresses only, it refuses to start unless you pass
  `--allow-public`, and it prints why.
- `bubbles healthcheck` checks that the HTTPS listener answers, using the CA in
  the data directory.
- `bubbles version` prints the build version.
