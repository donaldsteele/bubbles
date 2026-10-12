-- 0001_init: complete initial schema.
--
-- Conventions:
--   * IDs are ULIDs stored as TEXT; times are unix milliseconds (INTEGER).
--   * Tables are STRICT: bind Go int64/string/[]byte, never time.Time.
--   * SHA-256 digests (*_hash columns except pw_hash) are raw 32-byte BLOBs.
--   * JSON columns are TEXT checked with json_valid (bind string, not []byte).
--   * Booleans are INTEGER 0/1.
--   * Users are deactivated, never deleted: author/uploader references use the
--     default NO ACTION; ownership references cascade; attribution columns
--     (created_by, acked_by, ...) are nullable and SET NULL.
--   * schema_migrations is created by the migration runner, not here.

-- Exactly one row, seeded by store.Open. A factory reset recreates the
-- database (new id); clearing content increments epoch.
CREATE TABLE instance (
    id         TEXT    PRIMARY KEY,
    epoch      INTEGER NOT NULL DEFAULT 1 CHECK (epoch >= 1),
    created_at INTEGER NOT NULL
) STRICT;

CREATE TRIGGER instance_single_row BEFORE INSERT ON instance
WHEN (SELECT count(*) FROM instance) > 0
BEGIN
    SELECT RAISE(ABORT, 'instance holds exactly one row');
END;

CREATE TABLE users (
    id             TEXT    PRIMARY KEY,
    username       TEXT    NOT NULL UNIQUE COLLATE NOCASE
                   CHECK (length(username) BETWEEN 2 AND 32 AND username NOT GLOB '*[^a-z0-9._-]*'),
    display_name   TEXT    NOT NULL DEFAULT '' CHECK (length(display_name) <= 64),
    kind           TEXT    NOT NULL CHECK (kind IN ('human', 'app')),
    role           TEXT    NOT NULL CHECK (role IN ('admin', 'member', 'guest')),
    pw_hash        TEXT,
    avatar_file_id TEXT    REFERENCES files (id) ON DELETE SET NULL,
    status_text    TEXT    NOT NULL DEFAULT '' CHECK (length(status_text) <= 100),
    prefs          TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(prefs)),
    must_change_pw INTEGER NOT NULL DEFAULT 0 CHECK (must_change_pw IN (0, 1)),
    created_at     INTEGER NOT NULL,
    deactivated_at INTEGER
) STRICT;

CREATE TABLE sessions (
    id_hash         BLOB    PRIMARY KEY,
    user_id         TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind            TEXT    NOT NULL CHECK (kind IN ('web', 'handoff')),
    created_at      INTEGER NOT NULL,
    last_seen_at    INTEGER NOT NULL,
    idle_expires_at INTEGER NOT NULL,
    abs_expires_at  INTEGER NOT NULL,
    user_agent      TEXT    NOT NULL DEFAULT '',
    ip              TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX sessions_user ON sessions (user_id);

-- Tray app tokens (scopes: events:read, messages:read, notify).
CREATE TABLE device_tokens (
    id           TEXT    PRIMARY KEY,
    user_id      TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    token_hash   BLOB    NOT NULL UNIQUE,
    prefix       TEXT    NOT NULL,
    scopes       TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(scopes)),
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER,
    revoked_at   INTEGER
) STRICT;

CREATE INDEX device_tokens_user ON device_tokens (user_id);

CREATE TABLE invites (
    id         TEXT    PRIMARY KEY,
    code_hash  BLOB    NOT NULL UNIQUE,
    created_by TEXT    REFERENCES users (id) ON DELETE SET NULL,
    role       TEXT    NOT NULL CHECK (role IN ('admin', 'member', 'guest')),
    note       TEXT    NOT NULL DEFAULT '',
    expires_at INTEGER NOT NULL,
    max_uses   INTEGER NOT NULL DEFAULT 1 CHECK (max_uses >= 1),
    use_count  INTEGER NOT NULL DEFAULT 0 CHECK (use_count >= 0 AND use_count <= max_uses),
    revoked_at INTEGER,
    created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE password_resets (
    code_hash  BLOB    PRIMARY KEY,
    user_id    TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_by TEXT    REFERENCES users (id) ON DELETE SET NULL,
    expires_at INTEGER NOT NULL,
    used_at    INTEGER
) STRICT;

CREATE INDEX password_resets_user ON password_resets (user_id);

-- name: required and unique for public/private channels, NULL for DMs.
-- dm_key: sorted member ids joined by ',' (dm and group_dm only).
CREATE TABLE channels (
    id             TEXT    PRIMARY KEY,
    kind           TEXT    NOT NULL CHECK (kind IN ('public', 'private', 'dm', 'group_dm')),
    name           TEXT    CHECK (name IS NULL OR (length(name) BETWEEN 1 AND 64 AND name NOT GLOB '*[^a-z0-9-]*')),
    topic          TEXT    NOT NULL DEFAULT '' CHECK (length(topic) <= 250),
    description    TEXT    NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    dm_key         TEXT    UNIQUE,
    is_default     INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1)),
    retention_days INTEGER CHECK (retention_days IS NULL OR retention_days >= 1),
    created_by     TEXT    REFERENCES users (id) ON DELETE SET NULL,
    created_at     INTEGER NOT NULL,
    archived_at    INTEGER,
    CHECK (kind NOT IN ('public', 'private') OR name IS NOT NULL),
    CHECK (kind IN ('dm', 'group_dm') OR dm_key IS NULL)
) STRICT;

CREATE UNIQUE INDEX channels_name ON channels (name) WHERE kind IN ('public', 'private');

-- notify_level: omit it (NULL) on insert to get the per-kind default
-- ('all' for dm/group_dm, otherwise 'mentions'), applied by the trigger below.
CREATE TABLE memberships (
    channel_id         TEXT    NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    user_id            TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role               TEXT    NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'member')),
    joined_at          INTEGER NOT NULL,
    last_read_id       TEXT,
    notify_level       TEXT    CHECK (notify_level IN ('all', 'mentions', 'none')),
    alert_min_severity TEXT    NOT NULL DEFAULT 'warning' CHECK (alert_min_severity IN ('info', 'warning', 'critical')),
    muted_until        INTEGER,
    PRIMARY KEY (channel_id, user_id)
) STRICT;

CREATE INDEX memberships_user ON memberships (user_id);

CREATE TRIGGER memberships_default_notify AFTER INSERT ON memberships
WHEN new.notify_level IS NULL
BEGIN
    UPDATE memberships
    SET notify_level = COALESCE((
        SELECT CASE WHEN kind IN ('dm', 'group_dm') THEN 'all' ELSE 'mentions' END
        FROM channels WHERE id = new.channel_id
    ), 'mentions')
    WHERE channel_id = new.channel_id AND user_id = new.user_id;
END;

-- notify_level may be left NULL only on insert; an UPDATE must not clear it.
CREATE TRIGGER memberships_notify_not_null BEFORE UPDATE OF notify_level ON memberships
WHEN new.notify_level IS NULL
BEGIN
    SELECT RAISE(ABORT, 'notify_level must not be NULL');
END;

-- rowid is declared explicitly as INTEGER PRIMARY KEY (the rowid alias) so it
-- is stable across VACUUM / VACUUM INTO; messages_fts depends on it.
CREATE TABLE messages (
    rowid           INTEGER PRIMARY KEY,
    id              TEXT    NOT NULL UNIQUE,
    channel_id      TEXT    NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    author_id       TEXT    NOT NULL REFERENCES users (id),
    thread_root_id  TEXT    REFERENCES messages (id) ON DELETE CASCADE,
    kind            TEXT    NOT NULL DEFAULT 'text' CHECK (kind IN ('text', 'alert', 'system')),
    body            TEXT    NOT NULL DEFAULT '' CHECK (length(body) <= 8000),
    body_plain      TEXT    NOT NULL DEFAULT '',
    override_name   TEXT,
    mention_channel INTEGER NOT NULL DEFAULT 0 CHECK (mention_channel IN (0, 1)),
    mention_here    INTEGER NOT NULL DEFAULT 0 CHECK (mention_here IN (0, 1)),
    reply_count     INTEGER NOT NULL DEFAULT 0 CHECK (reply_count >= 0),
    last_reply_at   INTEGER,
    client_nonce    TEXT,
    edited_at       INTEGER,
    deleted_at      INTEGER,
    created_at      INTEGER NOT NULL,
    UNIQUE (author_id, client_nonce)
) STRICT;

CREATE INDEX messages_channel_thread ON messages (channel_id, thread_root_id, id);
CREATE INDEX messages_thread ON messages (thread_root_id, id);

CREATE VIRTUAL TABLE messages_fts USING fts5(
    body_plain,
    content = 'messages',
    content_rowid = 'rowid',
    tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TRIGGER messages_fts_ai AFTER INSERT ON messages
BEGIN
    INSERT INTO messages_fts (rowid, body_plain) VALUES (new.rowid, new.body_plain);
END;

CREATE TRIGGER messages_fts_ad AFTER DELETE ON messages
BEGIN
    INSERT INTO messages_fts (messages_fts, rowid, body_plain) VALUES ('delete', old.rowid, old.body_plain);
END;

CREATE TRIGGER messages_fts_au AFTER UPDATE OF body_plain ON messages
BEGIN
    INSERT INTO messages_fts (messages_fts, rowid, body_plain) VALUES ('delete', old.rowid, old.body_plain);
    INSERT INTO messages_fts (rowid, body_plain) VALUES (new.rowid, new.body_plain);
END;

CREATE TABLE mentions (
    message_id TEXT NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (message_id, user_id)
) STRICT;

CREATE INDEX mentions_user ON mentions (user_id);

-- Blob: data/files/<sha[0:2]>/<sha[2:4]>/<sha>, thumbnail <sha>.thumb.
-- sha256 is lowercase hex.
CREATE TABLE files (
    id            TEXT    PRIMARY KEY,
    sha256        TEXT    NOT NULL CHECK (length(sha256) = 64),
    size          INTEGER NOT NULL CHECK (size >= 0),
    mime          TEXT    NOT NULL,
    category      TEXT    NOT NULL CHECK (category IN ('image', 'video', 'audio', 'file')),
    original_name TEXT    NOT NULL CHECK (length(original_name) <= 255),
    width         INTEGER,
    height        INTEGER,
    has_thumb     INTEGER NOT NULL DEFAULT 0 CHECK (has_thumb IN (0, 1)),
    thumb_mime    TEXT,
    meta_stripped INTEGER NOT NULL DEFAULT 0 CHECK (meta_stripped IN (0, 1)),
    uploader_id   TEXT    NOT NULL REFERENCES users (id),
    created_at    INTEGER NOT NULL
) STRICT;

CREATE INDEX files_sha256 ON files (sha256);
CREATE INDEX files_uploader ON files (uploader_id);
CREATE INDEX files_created ON files (created_at);

CREATE TABLE attachments (
    message_id TEXT    NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    file_id    TEXT    NOT NULL REFERENCES files (id) ON DELETE CASCADE,
    position   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (message_id, file_id)
) STRICT;

CREATE INDEX attachments_file ON attachments (file_id);

CREATE TABLE reactions (
    message_id TEXT    NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    user_id    TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    emoji      TEXT    NOT NULL CHECK (length(CAST(emoji AS BLOB)) BETWEEN 1 AND 64),
    created_at INTEGER NOT NULL,
    PRIMARY KEY (message_id, user_id, emoji)
) STRICT;

CREATE TABLE pins (
    channel_id TEXT    NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    message_id TEXT    NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    pinned_by  TEXT    REFERENCES users (id) ON DELETE SET NULL,
    pinned_at  INTEGER NOT NULL,
    PRIMARY KEY (channel_id, message_id)
) STRICT;

CREATE INDEX pins_message ON pins (message_id);

-- user_id is the app's bot account.
CREATE TABLE apps (
    id            TEXT    PRIMARY KEY,
    user_id       TEXT    NOT NULL UNIQUE REFERENCES users (id) ON DELETE CASCADE,
    name          TEXT    NOT NULL,
    description   TEXT    NOT NULL DEFAULT '',
    owner_user_id TEXT    REFERENCES users (id) ON DELETE SET NULL,
    created_at    INTEGER NOT NULL,
    disabled_at   INTEGER
) STRICT;

-- Scopes: messages:read, messages:write, alerts:write, files:write,
-- channels:read, events:read, commands, dm:write, mcp.
-- channel_ids NULL = the channels the bot is a member of.
CREATE TABLE app_tokens (
    id           TEXT    PRIMARY KEY,
    app_id       TEXT    NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    token_hash   BLOB    NOT NULL UNIQUE,
    prefix       TEXT    NOT NULL,
    scopes       TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(scopes)),
    channel_ids  TEXT    CHECK (channel_ids IS NULL OR json_valid(channel_ids)),
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER,
    last_used_at INTEGER,
    revoked_at   INTEGER
) STRICT;

CREATE INDEX app_tokens_app ON app_tokens (app_id);

-- token_ciphertext is encrypted with a key derived from the poll secret and
-- deleted on claim.
CREATE TABLE pairing_requests (
    id               TEXT    PRIMARY KEY,
    app_name         TEXT    NOT NULL,
    description      TEXT    NOT NULL DEFAULT '',
    requested_scopes TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(requested_scopes)),
    code             TEXT    NOT NULL CHECK (length(code) = 6 AND code NOT GLOB '*[^0-9]*'),
    poll_secret_hash BLOB    NOT NULL,
    source_ip        TEXT    NOT NULL DEFAULT '',
    state            TEXT    NOT NULL DEFAULT 'pending'
                     CHECK (state IN ('pending', 'approved', 'denied', 'expired', 'claimed')),
    approved_app_id  TEXT    REFERENCES apps (id) ON DELETE SET NULL,
    token_ciphertext BLOB,
    created_at       INTEGER NOT NULL,
    expires_at       INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX pairing_requests_pending_code ON pairing_requests (code) WHERE state = 'pending';

CREATE TABLE commands (
    id          TEXT PRIMARY KEY,
    app_id      TEXT NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    name        TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 32 AND name NOT GLOB '*[^a-z0-9-]*'),
    description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 200),
    usage       TEXT NOT NULL DEFAULT '' CHECK (length(usage) <= 200)
) STRICT;

CREATE INDEX commands_app ON commands (app_id);

-- fields: JSON array of at most 20 {name, value, short} objects.
-- renotify_after_s NULL = the instance setting alert_renotify_s.
CREATE TABLE alerts (
    message_id       TEXT    PRIMARY KEY REFERENCES messages (id) ON DELETE CASCADE,
    app_id           TEXT    NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    dedupe_key       TEXT    NOT NULL CHECK (length(dedupe_key) BETWEEN 1 AND 200),
    severity         TEXT    NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    state            TEXT    NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'acked', 'resolved')),
    title            TEXT    NOT NULL CHECK (length(title) <= 200),
    fields           TEXT    NOT NULL DEFAULT '[]'
                     CHECK (json_valid(fields) AND json_type(fields) = 'array' AND json_array_length(fields) <= 20),
    link             TEXT    CHECK (link IS NULL OR link LIKE 'http://%' OR link LIKE 'https://%'),
    occurrences      INTEGER NOT NULL DEFAULT 1 CHECK (occurrences >= 1),
    first_seen_at    INTEGER NOT NULL,
    last_seen_at     INTEGER NOT NULL,
    last_notified_at INTEGER,
    renotify_after_s INTEGER CHECK (renotify_after_s IS NULL OR renotify_after_s >= 0),
    acked_by         TEXT    REFERENCES users (id) ON DELETE SET NULL,
    acked_at         INTEGER,
    resolved_by      TEXT    REFERENCES users (id) ON DELETE SET NULL,
    resolved_at      INTEGER
) STRICT;

CREATE UNIQUE INDEX alerts_open_dedupe ON alerts (app_id, dedupe_key) WHERE state != 'resolved';
CREATE INDEX alerts_app ON alerts (app_id);

-- app_id is the pseudo-app created for the webhook.
CREATE TABLE webhooks (
    id         TEXT    PRIMARY KEY,
    channel_id TEXT    NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    app_id     TEXT    NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    name       TEXT    NOT NULL,
    token_hash BLOB    NOT NULL UNIQUE,
    created_by TEXT    REFERENCES users (id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    revoked_at INTEGER
) STRICT;

CREATE INDEX webhooks_channel ON webhooks (channel_id);
CREATE INDEX webhooks_app ON webhooks (app_id);

CREATE TABLE push_subscriptions (
    id            TEXT    PRIMARY KEY,
    user_id       TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    endpoint      TEXT    NOT NULL UNIQUE,
    p256dh        TEXT    NOT NULL,
    auth          TEXT    NOT NULL,
    user_agent    TEXT    NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    last_ok_at    INTEGER,
    failure_count INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0)
) STRICT;

CREATE INDEX push_subscriptions_user ON push_subscriptions (user_id);

-- Global event log (seq = resume cursor), pruned after 7 days. channel_id and
-- user_id (user-scoped target) are deliberately not foreign keys: the log
-- outlives the rows it describes.
CREATE TABLE events (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    type       TEXT    NOT NULL,
    channel_id TEXT,
    user_id    TEXT,
    payload    TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(payload)),
    created_at INTEGER NOT NULL
) STRICT;

CREATE INDEX events_created ON events (created_at);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL CHECK (json_valid(value))
) STRICT;

-- actor_id is not a foreign key: audit rows outlive their actors.
CREATE TABLE audit_log (
    id          TEXT    PRIMARY KEY,
    at          INTEGER NOT NULL,
    actor_id    TEXT,
    actor_kind  TEXT    NOT NULL,
    actor_ip    TEXT    NOT NULL DEFAULT '',
    action      TEXT    NOT NULL,
    target_type TEXT    NOT NULL DEFAULT '',
    target_id   TEXT    NOT NULL DEFAULT '',
    detail      TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(detail))
) STRICT;

CREATE INDEX audit_log_at ON audit_log (at);
