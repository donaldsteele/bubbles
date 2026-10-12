# bubbles realtime protocol (WebSocket)

The REST API (`docs/openapi.yaml`) is the only way to change anything. The
WebSocket carries server-to-client events and a handful of client-to-server
ephemeral signals. Every wire type lives in `pkg/protocol` (`event.go`,
`ws.go`); this document is the normative description.

## Endpoint

```
wss://<host>:8443/api/v1/ws?resume=<seq>&instance=<instance_id>&epoch=<n>
```

| Query param | Required | Meaning |
|-------------|----------|---------|
| `resume`    | no | The last `seq` the client applied: `seq_head` of its `/api/v1/bootstrap` snapshot, or the `seq` of the last event frame it processed. Non-negative integer. |
| `instance`  | no | The `instance_id` the client's cached state belongs to (from bootstrap or a previous `hello`). |
| `epoch`     | no | The `epoch` the client's cached state belongs to. Positive integer. |

A malformed parameter answers `400` before the upgrade.

### Authentication and Origin

- Browsers authenticate with the normal session cookie (`__Host-bubbles_session`).
  Apps authenticate with `Authorization: Bearer bub_…` (no cookie, no Origin check). A bearer connection carries the token's channel allowlist and scopes: a token **without** `events:read` receives only its own `command.invoked` frames; a token **with** `events:read` receives the events of the channels its bot is a member of (and the allowlist admits), plus `command.invoked`.
- The desktop tray authenticates with a **device token** (`Authorization: Bearer bub_…` issued by `POST /api/v1/auth/device-login`, scopes `events:read`, `messages:read`, `notify`). A device connection sees exactly what its user sees (no channel allowlist, message content included). Revoking the device (`DELETE /api/v1/me/devices/{id}`), changing the user's password or deactivating the user closes it with `4009`.
- A cookie-authenticated upgrade **must** carry an `Origin` header (or, failing
  that, a `Referer`) whose host equals the request host; behind TLS the scheme
  must be `https`. Anything else is refused with `403` before the upgrade, which
  blocks cross-site WebSocket hijacking. Bearer clients are exempt.
- No principal: `401`. Authentication backend failure: `503`. Both use the REST
  error model (`{"error":{"code","message"}}`).
- The LAN source-address guard of the HTTPS listener applies as for every route.

## Connection lifecycle

1. The server registers the connection with the hub **before** anything else, so
   live events start queuing immediately.
2. It reads, in one consistent snapshot, the instance row and the event-log
   bounds (oldest retained `seq`, `seq_head`), then sends **`hello`**.
3. If `hello.resume` is `ok` and `resume < seq_head`, it **replays** every
   retained event with `resume < seq <= seq_head` that the user may see under the
   *current* visibility rules, in `seq` order.
4. It sends a **presence snapshot**: one `presence.changed` frame (`seq` 0) for
   every user the client may see who is not offline. Users not listed are
   offline.
5. It switches to **live** delivery: queued events with `seq <= seq_head` are
   skipped (the replay or the client's snapshot already covered them), so the
   client sees every visible event exactly once, in order, with no gap between
   replay and live.

If a retained event of the replay range is missing (a hole in `seq`, or the
log no longer holds it), the server does not send a partial replay: it closes
the socket with **1013** (`resync required`, try again later). A reconnect with
the same `resume` then gets `resync_required` from `hello`.

### `hello` (first frame, always)

```json
{"type":"hello","instance_id":"01J…","epoch":1,"seq_head":1234,"user_id":"01J…","server_version":"0.4.0","resume":"ok"}
```

| `resume` | When | Client reaction |
|----------|------|-----------------|
| `ok` | No `resume` given, or the resume point is still covered by the retained log (instance and epoch, when given, match). Replayed events follow. | Keep state, apply the frames. |
| `resync_required` | `epoch` differs (content was cleared), `resume` is ahead of `seq_head`, or an event after `resume` was already pruned (older than the 7-day retention or wiped). No replay. | Drop cached messages, unread state and drafts; re-fetch `/api/v1/bootstrap`; on this socket, ignore persisted frames with `seq` up to the new snapshot's `seq_head` and apply the rest. |
| `instance_changed` | `instance` differs (factory reset or a different server). No replay. | Clear all local state (`bubbles:*` storage, caches, push subscription) and hard-navigate to `/`. |

Precedence: an `instance` mismatch wins over an `epoch` mismatch, which wins
over the `resume` checks.

### Resume algorithm (server)

```
register subscription (live events start buffering)
read instance_id, epoch, oldest, head            -- one read transaction
if instance given and != instance_id:     instance_changed
elif epoch given and != epoch:            resync_required
elif no resume:                           ok (nothing to replay)
elif resume > head:                       resync_required
elif resume == head:                      ok
elif oldest == 0 or resume + 1 < oldest:  resync_required   -- a needed event is gone
else:                                     ok, replay (resume, head]
send hello; replay (filtered by current visibility, 500 rows per query)
send presence snapshot
live: for each queued frame: skip if 0 < seq <= head, else send
```

`seq` values are assigned by the single writer in commit order and are
contiguous; pruning always removes a prefix of the log, so `oldest` is enough
to decide whether the range after `resume` is complete. `seq_head` survives
pruning (it is the highest `seq` ever assigned) and equals bootstrap's
`seq_head` for the same moment.

## Server frames

### Event envelope

```json
{"seq":123,"type":"message.created","channel_id":"01J…","ts":"2026-03-01T12:00:00.000Z","data":{…}}
```

- `seq`: global, strictly increasing for persisted events; **`0` for ephemeral
  frames** (`typing`, `presence.changed`, `ephemeral.message`). Persisted frames
  arrive in `seq` order. A client stores the highest persisted `seq` it applied
  and passes it as `resume` when reconnecting.
- `channel_id`: present on channel-scoped events, omitted otherwise.
- `ts`: RFC 3339 UTC with milliseconds (when the event was written).
- `data`: the payload (below); `{}` when the event has none.

Replayed events carry their payload as currently stored: when a message is
deleted, earlier `message.*` payloads for it are rewritten to the tombstone and
`reaction.*` / `pin.*` payloads to `{"channel_id","message_id","deleted":true}`.

### `pong`

`{"type":"pong"}`: answer to a client `ping` frame.

### Event types, payloads and recipients

Recipients are computed by one function (`internal/events/visibility.go`) for
both live delivery and replay. "Members" means the channel's members at the
time of delivery (live) or of the replay. An admin is not a member of a private
channel or DM just by being admin. An app connection with a channel allowlist
never receives events of channels outside it.

| Type | `channel_id` | `data` (`pkg/protocol` type) | Recipients |
|------|--------------|------------------------------|------------|
| `message.created` | yes | `Message` | members |
| `message.updated` | yes | `Message` (edits, thread reply counts, alert re-renders) | members |
| `message.deleted` | yes | `MessageDeleted` `{id, channel_id, thread_root_id, deleted_at}` | members |
| `reaction.added`, `reaction.removed` | yes | `ReactionEvent` `{message_id, channel_id, user_id, emoji, count}` | members |
| `pin.added`, `pin.removed` | yes | `PinEvent` `{channel_id, message_id, user_id, pinned_at?}` | members |
| `channel.created` | yes | `ChannelEvent` `{channel, memberships?}` | members; for public channels also every non-guest |
| `channel.updated`, `channel.archived`, `channel.unarchived` | yes | `ChannelEvent` `{channel}` | members; for public channels also every non-guest |
| `channel.member_joined` | yes | `MemberJoinedEvent` `{channel_id, user_id, actor_id, channel, membership}` | members (including the new one) |
| `channel.member_left` | yes | `MemberLeftEvent` `{channel_id, user_id, actor_id}` | remaining members **and the departing user** |
| `membership.updated` | no | `Membership` | that user only (the row's user) |
| `read.updated` | no | `ReadEvent` `{channel_id, last_read_id, unread, mentions}` | that user only (all their devices) |
| `user.updated` | no | `User` (`deactivated: true` when deactivated) | every non-guest, the user themself, and guests sharing a channel with the user |
| `presence.changed` | no | `PresenceEvent` `{user_id, state: online\|idle\|offline}` (ephemeral) | same as `user.updated` |
| `typing` | yes | `TypingEvent` `{channel_id, user_id}` (ephemeral) | members except the typist |
| `alert.updated` | yes | `AlertUpdatedEvent` `{message, transition}`: the alert's `Message` after the update and what happened to it (`created`, `escalated`, `renotify_due`, `updated_other`, `acked`, `resolved`); see Alerts and Notifications below | members |
| `command.invoked` | no | `CommandInvoked` `{invocation_id, command, args, channel_id, thread_root_id, user:{id, username, display_name}}`; `seq` 0, not stored, not replayed | the owning app's connections only |
| `ephemeral.message` | no | `EphemeralMessage` `{invocation_id, channel_id, thread_root_id, author, body}`; `author` is the app's bot, or null for a notice from the server (command errors, "APP is offline", `/help`); `seq` 0, not stored | the invoking user's connections only |
| `settings.updated` | no | `SettingsUpdatedEvent` `{keys: [...]}` | everyone (clients re-fetch what they show) |
| `session.revoked` | no | `SessionRevokedEvent`, one of: `{session_id}`; `{all: true, except_session_id?}`; `{token_id, app_id}` (one app token); `{app_id, app_disabled: true}` (every token of a disabled app); `{token_id}` (one tray device; the payload carries the device id under `token_id`) | that user only; matching sockets are then closed with 4009 |

Side effects on sockets:

- `session.revoked` closes, within a second, every socket of that user that
  matches the payload with **4009**: a cookie socket by `session_id`, or by
  `all` except `except_session_id`; a bearer or device socket by `token_id`
  (when given), otherwise by `app_id` (every token of that app).
- `user.updated` with `deactivated: true` closes every socket of that user with
  **4009** (deactivation also revokes all sessions).
- `user.updated` carrying a new `role` changes what that user's open sockets
  receive from then on.

### Presence

- **online**: the user has a connection with activity within the last 5
  minutes. Activity = connecting, a `presence` frame with `state: "active"`,
  a `typing` frame, or a `focus` frame with `visible: true`.
- **idle**: connected, but no activity for 5 minutes or every connection last
  reported `state: "idle"`.
- **offline**: no connection for 30 s (grace period, so reconnects do not
  flap).

Transitions are published as `presence.changed` (`seq` 0); the server
re-evaluates every 5 s. Presence is coalesced: at most one `presence.changed`
per user every 5 s. A change that arrives inside that gap is held and published
by the first sweep after the gap, carrying the state current at that point.

### Typing

A `typing` client frame is relayed at most once per 3 s per user and channel
(extra frames are dropped silently) and only if the sender is a member of the
channel (and, for an app connection, the channel is on its allowlist).
Clients expire a typing indicator 6 s after the last frame.

## Client frames

Text frames of at most **4096 bytes** of JSON, one object each, no unknown
fields (`pkg/protocol.ClientFrame`):

| Frame | Meaning |
|-------|---------|
| `{"type":"typing","channel_id":"…"}` | The user is typing in the channel (send at most every 3 s). |
| `{"type":"presence","state":"active"}` / `"idle"` | User activity report (e.g. on input, and `idle` when the window is hidden for a while). |
| `{"type":"focus","channel_id":"…"\|null,"visible":true\|false,"ts":"…"?}` | Window visibility/focus. While any connection reports `visible: true` (or did within the last 60 s) the server sends no Web Push for that user. `ts` is optional client time (RFC 3339), informational only. |
| `{"type":"ping"}` | Application-level ping; answered with `{"type":"pong"}`. |

Unknown types, unknown fields, malformed values and binary frames close the
socket with **1008** (policy violation); frames over 4096 bytes close it with
**1009** (message too big).

**Rate limit:** each connection has a token bucket for client frames: it refills
at **10 frames/s** up to a burst of **20**. A frame that finds no token closes
the socket with **1008** (`rate limited`). Keep typing and presence reports well
under this; do not retry the frame on the same socket.

## Heartbeats

The server sends a WebSocket ping every 25 s. A connection that has not
answered with a pong within 60 s of the previous successful round trip is
dropped; a write that cannot complete within 60 s also drops it. Browsers answer
pings automatically.

## Flow control

Each connection has a send queue of 256 frames. If it is full when an event
arrives, the server stops sending further events to that socket and closes it
with **4008 `slow_consumer`**; the frames before the close are gap-free, so the
client simply reconnects with `resume=<last seq>`.

## Close codes

| Code | Reason | Sent when | Client reaction |
|------|--------|-----------|-----------------|
| 1000 / 1001 | – / `server shutting down` | normal close, server shutdown | reconnect with backoff and `resume` |
| 1008 | `invalid frame` / `text frames only` / `rate limited` | bad client frame, or client frame rate above 10/s (burst 20) | fix the client; reconnect |
| 1009 | – | client frame over 4096 bytes | fix the client; reconnect |
| 1013 | `resync required` | a retained event of the replay range is missing | reconnect with backoff and the same `resume`; the `hello` then says `resync_required` |
| **4008** | `slow_consumer` | send queue overflow | reconnect immediately with `resume` |
| **4009** | `session_revoked` | the socket's session was revoked (logout elsewhere, password change, admin action) or the user was deactivated | do not reconnect; show sign-in |
| **4010** | `server_reset` | factory reset | wipe all local state (`bubbles:*` storage, IndexedDB caches, push subscription, runtime caches); hard-navigate to `/` |
| **4011** | `content_cleared` | admin cleared all content (epoch + 1) | drop cached messages, unread state and drafts; reconnect (the `hello` says `resync_required`); re-fetch `/bootstrap` |

## Tray handoff

`GET /auth/handoff?code=<code>[&next=<path>]` (server root, not under `/api/v1`) redeems a one-time
handoff code (single use, 60 seconds), starts a normal web session and answers `303`. The
optional `next` is a same-origin absolute path such as `/c/<channel id>`; the redirect goes
there. Anything else (no leading `/`, `//host`, backslashes, control characters, a scheme) is
ignored and the redirect is `/`. An unknown, used or expired code redirects to
`/login?error=handoff` without a cookie.

## Alerts

An app posts an alert with `POST /api/v1/alerts` (scope `alerts:write`). The
alert is a `kind: alert` message whose `alert` object carries severity, state,
occurrences and timestamps. Every alert change is one transaction with its events:

| What happened | Events | `alert.updated` transition |
|---------------|--------|----------------------------|
| New alert (no un-resolved alert for the app and `dedupe_key`) | `message.created` (kind `alert`) | none: a new alert message is implicitly `created` |
| Repeat of an open alert, higher severity | `message.updated`, `alert.updated` | `escalated` |
| Repeat, open and un-acknowledged, last notification older than `renotify_after_s` | `message.updated`, `alert.updated` | `renotify_due` |
| Any other repeat (inside the interval, acknowledged, or lower severity) | `message.updated`, `alert.updated` | `updated_other` |
| A member acknowledged it | `alert.updated` | `acked` |
| Resolved by id or by key | `message.created` (kind `system`, thread reply "Resolved by <name>: <text>"), `message.updated` (root reply count), `alert.updated` | `resolved` |

A repeat re-renders the message body from the latest post and keeps the higher
severity; `renotify_after_s` defaults to the `alert_renotify_s` setting and 0 turns
renotification off. A key that was resolved opens a new alert the next time.
Only `created`, `escalated` and `renotify_due` can notify (see Notifications).

## Notifications

Whether a message or alert event notifies a user is one pure function,
`pkg/notifyrules.Decide`, mirrored in `web/src/lib/notify/rules.ts` and tested
on the shared vectors in `testdata/notify-rules.json`. In order, the first rule
that matches decides:

1. system messages never notify;
2. never the author;
3. a muted membership (`now < muted_until`) gets nothing, critical alerts
   included;
4. alert messages notify only for the transitions `created`, `escalated` and
   `renotify_due` (a new alert message is `created`; `alert.updated` carries its
   `transition`), and only when the alert severity is at least the member's
   `alert_min_severity`;
5. DMs and group DMs notify;
6. `notify_level` `all` notifies;
7. `mentions` notifies when the user is in `mentions.user_ids`, for `@channel`, for
   `@here` if the user is online, and for critical alerts;
8. `none` does not.

**Web Push** is the one place the server decides: `internal/notify` evaluates the
rules for every human member of the channel except the author (apps never get
notifications) on each `message.created` (text and alert) and `alert.updated`
event. A notifiable user gets a push only when the admin enabled `web_push_enabled`,
the user has a subscription, and no connection of theirs reported `focus`
`visible: true` within the last 60 seconds. The message is encrypted (RFC 8291),
TTL 3600, urgency `high` for critical alerts. Payload: `{title, channel_id,
message_id}` with title `"<sender> in #<channel>"` (DMs: `"<sender>"`); with the
`push_payload` setting `full` a `body` excerpt of at most 120 characters is added.
A 404 or 410 answer deletes the subscription, as do 5 consecutive failures.

Subscription endpoints are user-supplied URLs the server POSTs to, so they must
be `https` URLs (port 443, no credentials) on `fcm.googleapis.com`,
`*.push.services.mozilla.com`, `*.notify.windows.com`, `web.push.apple.com` or
`*.push.apple.com`; the sender additionally refuses to connect to private,
loopback or link-local addresses after DNS resolution and never follows redirects.

## Retention

Events are kept for 7 days (pruned hourly, oldest first). Resuming from an older
point yields `resync_required`.

## Slash commands (two-way apps)

An app registers commands with `PUT /api/v1/apps/self/commands`. When a person
runs `/name args`, the client calls `POST /api/v1/commands/invoke`; the server
publishes `command.invoked` to the app's live connections only (the app must be
connected, else the call is `503` and the person sees an ephemeral
"APP is offline" notice). The app answers within 15 minutes with
`POST /api/v1/commands/invocations/{id}/respond {text, ephemeral}`: an
ephemeral answer arrives as `ephemeral.message` on the invoker's connections
only and is never stored; otherwise the bot posts a normal message. Built-in
commands (`/topic`, `/invite`, `/leave`, `/mute`, `/me`, `/help`) run on the
server; their problems and `/help` output also arrive as `ephemeral.message`.
