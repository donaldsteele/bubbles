# Integrations

bubbles has four ways in for other software. Pick the lightest one that does the job.

| You want to | Use | Needs |
|-------------|-----|-------|
| Post from a script or a tool that can call a URL (Slack-style senders, cron, monitoring) | [Incoming webhooks](#incoming-webhooks) | A webhook URL from the admin UI |
| Do anything programmatically (messages, alerts, files, commands, events) | [REST API](#rest-api) with an app token | An app token with scopes |
| Write a Go program | [Go SDK](#go-sdk) (`pkg/bubblesclient`) | A connection string, or pairing |
| Let an AI assistant use bubbles | MCP server (see [mcp.md](mcp.md)) | An app token with the `mcp` scope |

Everything is HTTPS with the server's local CA. Unless you have installed the CA, pin its
SHA-256 fingerprint instead of disabling verification (see [Pairing](#pairing)). The wire
formats are in [openapi.yaml](openapi.yaml) and [protocol.md](protocol.md).

## Incoming webhooks

An admin creates a webhook for one channel in the admin UI and gets a URL once:
`https://host:8443/hooks/bwh_...`. The token in the URL is the only credential; keep it
secret and revoke it in the admin UI if it leaks. Each webhook accepts 30 posts per minute
(burst 10); beyond that the answer is `429` with `Retry-After`. The answer to a good post is
`200` with the text `ok`. Bodies are limited to 256 KiB.

A webhook message never mentions anyone, whatever the payload says.

### Native alert

```sh
curl --cacert bubbles-ca.pem https://192.168.1.20:8443/hooks/bwh_XXXX \
  -H 'Content-Type: application/json' \
  -d '{"alert": {"severity": "warning", "title": "Backup is late",
       "text": "Last success 3 days ago", "dedupe_key": "backup:nas",
       "fields": [{"name": "Host", "value": "nas", "short": true}]}}'
```

`severity` is `info`, `warning` or `critical`. Posting again with the same `dedupe_key`
updates one message instead of adding a new one (see [netdiag.md](netdiag.md) for the
lifecycle). Webhooks cannot resolve alerts; use the REST API or the SDK for that.

### Slack-compatible

Anything that can post to a Slack incoming webhook works by changing the URL. The supported
subset is `text` (mrkdwn), `username`, `icon_emoji`, `attachments[]` (`color`, `pretext`,
`title`, `title_link`, `text`, `fields`, `footer`) and `blocks[]` (`header`, `section`,
`divider`, `context`, `image`, `actions` buttons as links). Other blocks leave an
`(unsupported block)` note. The legacy form encoding (`payload=<json>`) is accepted too.

```sh
curl --cacert bubbles-ca.pem https://192.168.1.20:8443/hooks/bwh_XXXX \
  -d 'payload={"text":"Deploy *finished* in <https://ci.local/run/42|run 42>"}'
```

## REST API

Create an app in the admin UI (Admin > Apps) and issue a token with the scopes it needs.
The token (`bub_...`) is shown once together with a connection string. Send it as a bearer
token.

```sh
export B=https://192.168.1.20:8443 H='Authorization: Bearer bub_XXXX'

# who am I
curl --cacert bubbles-ca.pem -H "$H" $B/api/v1/apps/self

# post a message (messages:write)
curl --cacert bubbles-ca.pem -H "$H" -H 'Content-Type: application/json' \
  -d '{"body": "Hello **team**", "nonce": "9f1c0b"}' \
  $B/api/v1/channels/01J.../messages

# post or update an alert (alerts:write); channel is an id or a name
curl --cacert bubbles-ca.pem -H "$H" -H 'Content-Type: application/json' \
  -d '{"channel": "alerts", "severity": "critical", "title": "Disk full",
       "text": "/var is at 99%", "dedupe_key": "disk:web1", "renotify_after_s": 3600}' \
  $B/api/v1/alerts

# resolve it
curl --cacert bubbles-ca.pem -H "$H" -H 'Content-Type: application/json' \
  -d '{"dedupe_key": "disk:web1", "text": "freed 40 GB"}' $B/api/v1/alerts/resolve

# upload a file (files:write), then attach it with {"file_ids": ["..."]}
curl --cacert bubbles-ca.pem -H "$H" -F file=@report.pdf $B/api/v1/files

# register slash commands (commands)
curl --cacert bubbles-ca.pem -H "$H" -X PUT -H 'Content-Type: application/json' \
  -d '[{"name": "echo", "description": "Repeat text", "usage": "/echo <text>"}]' \
  $B/api/v1/apps/self/commands
```

Errors use one shape: `{"error": {"code": "...", "message": "...", "details": {...}}}`
with codes `bad_request`, `unauthorized`, `forbidden`, `not_found`, `conflict`, `too_large`,
`unsupported_media`, `rate_limited`, `insufficient_storage`, `unavailable`, `internal`.
A resource the token cannot see is always `404`, never `403`.

### Scopes

| Scope | Allows |
|-------|--------|
| `messages:read` | Read messages, threads, pins and search in channels the app's bot is in; list commands |
| `messages:write` | Post, edit and delete the bot's messages, reactions, pins |
| `alerts:write` | Post, ack and resolve alerts; list alerts |
| `files:write` | Upload files |
| `channels:read` | List channels and members |
| `events:read` | Receive channel events over the WebSocket (without it the socket only carries the app's own `command.invoked` frames) |
| `commands` | Register slash commands and answer invocations |
| `dm:write` | Start direct messages |
| `mcp` | Use the MCP endpoint (combined with the scopes of the tools used) |

A token can also carry a channel allowlist; the app then never sees or touches anything
outside it. An app's bot user only acts in channels it is a member of.

### Rate limits

| What | Limit |
|------|-------|
| Any request with an app token | 10 per second, burst 30, per token |
| Posting messages | 10 per second, burst 20, per user |
| Alert ingest | 60 per minute, burst 20, per app |
| Each incoming webhook | 30 per minute, burst 10 |
| Pairing requests | 5 per minute per IP, 20 pending |

Over a limit the server answers `429 rate_limited` with a `Retry-After` header in seconds.
Honour it; the SDK's `AlertQueue` does.

### Realtime events

`GET /api/v1/ws` with `Authorization: Bearer bub_...` streams events (messages, alerts,
reactions, `command.invoked`, ...) as JSON frames. Reconnect with
`?resume=<last seq>&instance=<id>&epoch=<n>` to replay what you missed. See
[protocol.md](protocol.md). The Go SDK does all of this for you.

## Go SDK

```sh
go get github.com/donaldsteele/bubbles@latest
```

The package `pkg/bubblesclient` depends only on the standard library,
`github.com/coder/websocket` and `github.com/brutella/dnssd`; its wire types live in
`pkg/protocol` (standard library only).

```go
cfg, err := bubblesclient.ParseConnectionString(os.Getenv("BUBBLES_CONN"))
// bubbles://192.168.1.20:8443/?token=bub_...&fp=sha256:<64 hex>
if err != nil { log.Fatal(err) }
c, err := bubblesclient.New(cfg) // TLS trusts only the pinned CA
if err != nil { log.Fatal(err) }

// Fire and forget, surviving server restarts and rate limits.
q := bubblesclient.NewAlertQueue(c, 0)
defer q.Close()
q.SetOnStop(func(err error) { log.Printf("re-pair needed: %v", err) })
q.Enqueue(protocol.AlertInput{
    Channel: "alerts", Severity: "warning", Title: "Printer offline",
    DedupeKey: "printerOffline:192.168.1.50",
})
q.EnqueueResolve("printerOffline:192.168.1.50", "Printer is reachable again")
q.Flush(ctx)

// Direct calls.
msg, err := c.PostMessage(ctx, channelID, protocol.NewMessage{Body: "hi"})
err = c.ResolveAlert(ctx, "printerOffline:192.168.1.50", "")

// Events with automatic reconnect, resume and jittered backoff.
events, err := c.Events(ctx)
for ev := range events {
    switch ev.Type {
    case "message.created": // ev.Data is a protocol.Message
    case bubblesclient.EventResyncRequired: // missed events: refetch state
    }
}
if err := c.EventsErr(); err != nil { /* ErrUnauthorized: re-pair */ }
```

Errors are comparable with `errors.Is`:

| Error | Meaning | What to do |
|-------|---------|------------|
| `ErrUnauthorized` | Token revoked, or the server was factory-reset | Stop; pair again |
| `ErrServerIdentityChanged` | The server's certificate chain no longer ends in the pinned CA (CA rotated, or an impostor) | Never trust automatically; verify and pair again |
| `ErrRateLimited` | HTTP 429 (`APIError.RetryAfter` has the delay) | Back off |
| `ErrUnavailable` | Network failure or HTTP 5xx | Retry later |

`*bubblesclient.APIError` carries the status, code, message and details of any non-2xx
answer. Examples: [`examples/netdiag-sender`](../examples/netdiag-sender) and
[`examples/echo-bot`](../examples/echo-bot).

## Pairing

Pairing gets an app a token without anyone copying secrets around.

1. **Find the server.** `bubblesclient.Discover(ctx, 3*time.Second)` browses mDNS for
   `_bubbles._tcp` (TXT: `v=1`, `fp=<first 16 hex of the CA SHA-256>`, `onboard=<http port>`,
   `path=/`). Or just use the address you were given.
2. **Fetch and confirm the CA.** `FetchCA(ctx, "http://host:8080")` returns the CA and its
   fingerprint over the unauthenticated onboarding port. Show
   `FormatFingerprint(fp)` to the user and have them compare it with the admin UI.
3. **Request.** `RequestPairing(ctx, "https://host:8443", fp, protocol.PairingRequest{AppName: "netdiag", RequestedScopes: []string{"alerts:write"}})`
   pins the CA from here on. Show `p.Code()` (6 digits) to the user.
4. **Approve.** An admin sees the pending request with the code, the requester's IP and the
   CA fingerprint, picks scopes and channels (optionally creating `#<app>-alerts`) and
   approves.
5. **Wait.** `cfg, err := p.Wait(ctx)` polls with backoff and returns a `Config` with the
   token and pinned fingerprint. The token is handed out exactly once, on the first poll
   after approval, so store it right away (`cfg.ConnectionString()`). Denied or expired
   requests return `ErrPairingDenied` / `ErrPairingExpired`. Requests expire after 10
   minutes.

Alternatively an admin creates an app and token directly and hands the connection string
(text or QR) to the app.

## MCP

AI assistants can use bubbles through its MCP server. This is documented in
[mcp.md](mcp.md); it needs an app token with the `mcp` scope.
