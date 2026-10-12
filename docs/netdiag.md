# netdiag integration

netdiag posts its Notices to bubbles as alerts. [`examples/netdiag-sender`](../examples/netdiag-sender)
is the reference implementation of this contract.

## Setup

1. Pair netdiag with the server (see [integrations.md](integrations.md#pairing)) or paste a
   connection string created in the admin UI. The token needs `alerts:write`.
2. Pick the channel for alerts (the pairing approval can create `#netdiag-alerts`).
3. Send Notices through `bubblesclient.NewAlertQueue`.

```sh
# one JSON Notice per line on stdin
netdiag-sender --conn "$BUBBLES_CONN" --channel netdiag-alerts < notices.jsonl

# first run: pair interactively and keep the connection string
netdiag-sender --pair https://192.168.1.20:28225 --onboard http://192.168.1.20:28226 \
  --save ~/.netdiag-bubbles --demo
```

Exit status 3 means the token was revoked or the server identity changed: pair again.

## Mapping

A netdiag `Notice{Trigger, Severity, IP, Title, Message, At, Key}` becomes an
`AlertInput`:

| Notice | AlertInput | Notes |
|--------|------------|-------|
| `Severity` `info` | `severity: "info"` | |
| `Severity` `warn` | `severity: "warning"` | |
| `Severity` `critical` | `severity: "critical"` | |
| `Title` | `title` | Falls back to `Trigger` |
| `Message` | `text` | |
| `IP` | `fields: [{name: "IP", value, short: true}]` | Omitted when empty |
| `Trigger` | `fields: [{name: "Trigger", value, short: true}]` | |
| `Key` | `dedupe_key` | Falls back to `"<trigger>:<ip>"` |
| `At` | `occurred_at` | RFC 3339 |

Suggested defaults per trigger:

| Trigger | Severity |
|---------|----------|
| `printerOffline` | warning |
| `deviceOffline` | info |
| `supplyLow` | warning |
| `printerAlert` | the printer's own severity |
| `newDevice` | info |
| `ipConflict` | critical |
| `healthFailed` | critical |

## Dedupe keys and lifecycle

Alerts with the same `dedupe_key` are one alert. The first post creates a message; later
posts update it in place (occurrence count, last seen) instead of adding noise. People are
notified again only on a real change: a higher severity (`escalated`), the renotify
interval elapsing (`renotify_after_s`), or a re-open after a resolve. Choose keys that name
the problem, not the occurrence: `printerOffline:192.168.1.50`, not one containing a
timestamp. For problems with several independent aspects on one device add a suffix,
for example `supplyLow:192.168.1.50:black`.

## Resolve semantics

When netdiag reports "OK again" for a key, call `ResolveAlert(key)` (the sender maps a
Notice with `severity: "ok"` or `"ok": true` to this). The alert is marked resolved, a system
note is added to its thread, and a later problem with the same key opens a fresh alert.
Resolving a key that has no open alert is harmless. Resolve is keyed only by `dedupe_key`,
so use the same key for the problem and its recovery.

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
