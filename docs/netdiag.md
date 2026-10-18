# netdiag integration

netdiag posts the problems it finds on the local network to bubbles as alerts. From v0.6
netdiag has its own client: **Settings > Team chat (bubbles)** pairs with the server, posts
alerts and closes them again when the problem is fixed. The first part of this page says
what netdiag v0.6 sends.

[`examples/netdiag-sender`](../examples/netdiag-sender) is a separate, smaller sender for
netdiag-style Notice lines. It is described [at the end](#the-example-sender).

## netdiag v0.6

### Pairing and scopes

netdiag pairs under the app name `netdiag` (see [integrations.md](integrations.md#pairing))
with the description `netdiag on <PC name>`. It asks for these scopes:

| Scope | What netdiag uses it for |
|-------|--------------------------|
| `alerts:write` | Post alerts, and resolve them by `dedupe_key` |
| `channels:read` | List the channels its bot is in: to pick the alert channel, and to tell the two kinds of `409` apart |
| `messages:write` | Plain messages in the alert channel (see [Messages](#messages)) |
| `files:write` | Nothing yet. netdiag asks for it now so that a later version can send files without a second pairing |

netdiag does not ask for `messages:read`, so it never lists alerts or reads the channel.
The admin who approves adds netdiag's bot to channels (the approval can create
`#netdiag-alerts`). netdiag posts to one of them: the only one, or the one picked in its
wizard.

### Severity per trigger

netdiag has one severity map for its own banners and for chat. Its `warn` is sent as
`warning`.

| Trigger | Severity in chat | Notes |
|---------|------------------|-------|
| `printerOffline` | warning | |
| `deviceOffline` | info | Off by default in netdiag |
| `supplyLow` | warning | |
| `printerAlert` | critical when the printer marks the alert `critical` or `error`, else warning | |
| `ipConflict` | critical for a `mac_flap` where the devices were seen taking turns on the address; info for `mac_multi_ip` (one device on several addresses); warning for the others (`mac_flap` without a swap, `duplicate_name`) | |
| `healthFailed` | critical for the Router check (`gateway`), warning for every other check | |
| `newDevice` | info | Sent as a plain message, not an alert |
| `netdiag:test` | info | The test alert from **Send test alert** |
| `netdiagLink` | not sent | netdiag's own "lost the network" notice; local only |
| `unknownDevice` | info | Planned for netdiag v0.9, not in v0.6 |

netdiag sends an alert only when its severity is at least the **Team chat alert level** set in
netdiag: *Problems and warnings* (warning and critical, the default), *Only urgent problems*
(critical) or *Everything, including notes* (info too). The test alert and resolves go
whatever the level. netdiag posts alerts only while a scan in Live mode watches the network;
a resolve goes from any scan.

### What an alert says

| AlertInput | Value |
|------------|-------|
| `title` | The problem and what it is about, for example "Printer offline: Front Desk HP (192.168.1.50)" or "Health check failed: Router" |
| `text` | The problem's message, then "What to try:" with up to 3 numbered steps |
| `fields` | `Device`, `Address`, `MAC address`, `Maker`, `Model`, `Since`, then `Check` for a health check or `Supply` for a low supply |
| `link` | The device's own web page, only when it is `http` or `https` on the device's own address |
| `dedupe_key` | The problem's key (see [netdiag's keys](#netdiags-dedupe-keys)) |
| `renotify_after_s` | `0`: an open card never notifies again by itself |
| `occurred_at` | When the problem started |

About the fields:

- `Device` is netdiag's best name for the device, `Address` its IP address. A health check
  has neither.
- An IP conflict with several MACs has `MAC addresses` (comma-separated) instead of
  `MAC address`.
- `Since` is the time the problem started, on the netdiag computer's clock ("10:42").
- Empty fields are left out.

### netdiag's dedupe keys

A key is `<trigger>|<id>|<part>`, joined with `|`.

- `<id>` is the device's MAC in upper case. While the MAC is unknown it is `ip:<address>`.
  netdiag also uses the `ip:` form while another device behind the same MAC (a range
  extender) has the MAC key open.
- `<part>` tells apart several problems of one trigger on one device. It is empty for a
  device that is offline, so that key ends in `|`. A printer alert's part has a `|` of its
  own.

| Trigger | Key |
|---------|-----|
| `printerOffline`, `deviceOffline` | `printerOffline\|00:07:4D:11:22:33\|` or `printerOffline\|ip:192.168.1.50\|` |
| `supplyLow` | `supplyLow\|<id>\|<hash>`: the first 8 hex digits of the SHA-256 of the supply's description, with `.2`, `.3` and so on for later supplies with the same description |
| `printerAlert` | `printerAlert\|<id>\|<code>\|<hash of the description>`, for example `printerAlert\|00:07:4D:11:22:33\|hrPrinterDetectedErrorState.doorOpen\|1a2b3c4d` |
| `ipConflict` | `ipConflict\|<kind>\|<anchor>`: the shared address for `mac_flap` (`ipConflict\|mac_flap\|192.168.1.50`), the MAC for `mac_multi_ip`, a hash of the name for `duplicate_name` |
| `healthFailed` | `healthFailed\|\|<check>`, for example `healthFailed\|\|gateway` |
| test alert | `netdiag:test` |

Keys are at most 200 characters, bubbles' limit for `dedupe_key`. netdiag shortens a longer
key to `<trigger>|<id>|h:<hash>` (the first 32 hex digits of the SHA-256 of the full key), or
to `<trigger>||h:<hash>` when that is still too long. A new device sends no alert, so it has
no card key.

### Resolve

When a problem is fixed, netdiag resolves the same card by `dedupe_key`
(`POST /api/v1/alerts/resolve`). It resolves only cards it posted, or is about to post; a
problem that never reached chat is fixed without a word there.

The resolve note is "Fixed at 10:42 (lasted 12 min)." in the netdiag computer's local time,
then the message of netdiag's "OK again" notice, for example "Fixed at 10:42 (lasted 12 min).
Printer FRONTDESK-HP (192.168.1.50) answers again." The duration reads "less than a minute",
"12 min", "2 h", "2 h 5 min", "1 day" or "8 days 2 h". "(lasted ...)" is left out when netdiag
does not know when the problem started. bubbles adds the note to the card's thread as
`Resolved by <bot name>: <note>`.

netdiag resolves a card with other notes in these cases:

| Note | When |
|------|------|
| `Moved to #<channel>.` | Someone picked another alert channel in netdiag. The card is posted again in the new channel |
| "netdiag stopped sending alerts here." | Someone switched **Send alerts to team chat** off |
| "netdiag has not been on this network for 7 days." | netdiag dropped the problem because it has not been on its network for 7 days |
| "Test finished." | The test alert, right after it was posted |

### Messages

netdiag posts these as plain messages, not alerts:

- "netdiag on FRONTDESK-PC is connected. It posts alerts here while it is watching the local
  network." once sending starts.
- `New device on the local network: <name> (<address>).` for a new device, at the level
  *Everything, including notes* only. Nothing would ever close a card for it.
- "…and 7 more problems. Open netdiag on FRONTDESK-PC to see them all." when netdiag starts
  sending, or starts watching a network, and more than 10 open problems have no card yet.
  netdiag posts the 10 most severe as alerts.

### Delivery and errors

netdiag keeps what it still has to send in its own outbox file, `chat-outbox.json`. It does
not use the SDK's `AlertQueue`.

- A post waits at most 60 minutes, a resolve 7 days. The outbox holds at most 500 items.
- A newer post for a key replaces one that still waits.
- A fix that comes while the post that would open the card still waits drops both: nobody
  was told, so nobody is told it is fixed. When that post is already on its way, the resolve
  waits for it and goes only if the post gets through.
- A waiting post that only updates a card that is already open is dropped, and the card is
  resolved.
- After a restart a waiting post goes only if its problem is still open.
- `occurred_at` stays the time the problem started, also when the post is delivered late
  from the outbox. bubbles records the earlier of `occurred_at` and the time the post
  arrives.
- `429`, `5xx` and network failures are retried with backoff from about 2 to 60 seconds; a
  longer `Retry-After` on a `429` is honoured.
- `401` or a changed server identity stops sending. netdiag's Settings then ask the person to
  reconnect.
- `404` or `403` on a post: netdiag stops posting to that channel (its bot may not post
  there) and keeps the items. On a resolve the item is dropped: there is no card of netdiag's
  to close.
- `400` and other refusals: the item is dropped. netdiag's Activity log says "The chat server
  turned down an alert. Ask your chat admin to check bubbles' log."

On `409` netdiag lists its channels to find out which of bubbles' two conflicts it is:

- The alert channel is archived: bubbles still lists it, with `archived_at` set, but netdiag
  counts only channels that are not archived. netdiag stops posting there and keeps the
  items, resolves too, until someone picks another channel.
- The key's card is still open in another channel: netdiag resolves that card by key with
  `Moved to #<channel>.` and posts the alert once more. If that resolve gets `409` too, or
  the second post gets `409` again, netdiag drops the post. Its Activity log says
  `Team chat could not move an alert out of the archived channel #<old channel>.` ("an
  archived channel" when it does not know the old name).
- While the alert channel itself is not archived, a resolve that gets `409` is dropped, with
  `Team chat could not close an alert in archived channel #<old channel>.` ("an archived
  channel" when it does not know the old name) in the Activity log.

**Send test alert** posts the test alert and resolves it directly, not through the outbox.
When the post gets the `409` for a key open in another channel, netdiag resolves the old test
card with "Test finished." and posts the test once more.

## Dedupe keys and lifecycle

These rules are bubbles' own; they apply to netdiag and to any other sender.

Alerts with the same `dedupe_key` are one alert. The first post creates a message; later
posts update it in place (occurrence count, last seen) instead of adding noise. People are
notified again only on a real change: a higher severity (`escalated`), the renotify
interval elapsing (`renotify_after_s`), or a re-open after a resolve. Choose keys that name
the problem, not the occurrence: `printerOffline|00:07:4D:11:22:33|` as netdiag sends it, or
`printerOffline:192.168.1.50`, not one containing a timestamp. For problems with several
independent aspects on one device add a part, for example `supplyLow:192.168.1.50:black`.

## Resolve semantics

When a problem is fixed, call `ResolveAlert(key)` with the key of the problem. The alert is
marked resolved, a system note is added to its thread, and a later problem with the same key
opens a fresh alert. Resolving a key whose alert is already resolved returns it unchanged; a
key the app never posted is `404`, which the `AlertQueue` ignores. Resolve is keyed only by
`dedupe_key`, so use the same key for the problem and its recovery.

## The example sender

[`examples/netdiag-sender`](../examples/netdiag-sender) reads netdiag-style Notice JSON lines
and sends them through `bubblesclient.NewAlertQueue`. It is a reference for the SDK, not what
netdiag v0.6 runs.

### Setup

1. Pair it with the server (see [integrations.md](integrations.md#pairing)) or paste a
   connection string created in the admin UI. It asks for `alerts:write` only.
2. Pick the channel for alerts with `--channel` (default `netdiag-alerts`).

```sh
# one JSON Notice per line on stdin
netdiag-sender --conn "$BUBBLES_CONN" --channel netdiag-alerts < notices.jsonl

# first run: pair interactively and keep the connection string
netdiag-sender --pair https://192.168.1.20:28225 --onboard http://192.168.1.20:28226 \
  --save ~/.netdiag-bubbles --demo
```

Exit status 3 means the token was revoked or the server identity changed: pair again.

### Mapping

A Notice `{trigger, severity, ip, title, message, at, key, ok}` becomes an `AlertInput`:

| Notice | AlertInput | Notes |
|--------|------------|-------|
| `severity` `info` | `severity: "info"` | Any other value is `info` too |
| `severity` `warn` or `warning` | `severity: "warning"` | |
| `severity` `critical` | `severity: "critical"` | |
| `title` | `title` | Falls back to `trigger` |
| `message` | `text` | |
| `ip` | `fields: [{name: "IP", value, short: true}]` | Omitted when empty |
| `trigger` | `fields: [{name: "Trigger", value, short: true}]` | |
| `key` | `dedupe_key` | Falls back to `"<trigger>:<ip>"` |
| `at` | `occurred_at` | RFC 3339 |

The sender keeps the severity each Notice carries; it has no per-trigger defaults. A Notice
with `"ok": true` or `severity: "ok"` resolves the key with the Notice's `message` as the
note.

## AlertQueue

`NewAlertQueue(client, max)` keeps alerts that cannot be delivered yet and sends them in the
background:

- bounded (default 1000 distinct keys); when full the oldest pending key is dropped and
  counted in `Dropped()`
- coalesces by `dedupe_key`: only the newest state of a key is sent, 50 updates of one
  alert during an outage become one request, in the position the key was first queued
- retries 429, 5xx and network failures with exponential backoff, using `Retry-After` when
  the server sends it
- a resolve queued after a post replaces it (the alert never opened, so nothing to resolve)
- stops on `ErrUnauthorized` or `ErrServerIdentityChanged`: the cause is in `Err()` and is
  passed to the `SetOnStop` callback; the app must re-pair
- other rejections (a malformed alert) are dropped and counted, not retried forever

```go
q := bubblesclient.NewAlertQueue(c, 0)
defer q.Close()
q.SetOnStop(func(err error) { notifyOperator(err) })
q.Enqueue(alert)                              // PostAlert in the background
q.EnqueueResolve(key, "Printer is back")      // ResolveAlert in the background
q.Flush(ctx)                                  // optional: wait until delivered
```
