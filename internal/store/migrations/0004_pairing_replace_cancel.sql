-- Pairing requests gain two things: the app that asks can cancel its request
-- (state 'cancelled'), and a re-pairing can name the app it was paired as
-- before (replaces_app_id) so the admin can reconnect that app instead of
-- creating a second one (replaced = 1 once the approval did).
--
-- SQLite cannot change a CHECK constraint, so the table is rebuilt: create
-- the new table, copy every row, drop the old one, rename the new one and
-- recreate the partial index on code. The runner applies a migration inside
-- one transaction, where PRAGMA foreign_keys cannot be switched off, so the
-- rebuild runs with foreign keys enforced. That is safe here: no table
-- references pairing_requests, and the copied approved_app_id values already
-- satisfy their foreign key.
CREATE TABLE pairing_requests_new (
    id               TEXT    PRIMARY KEY,
    app_name         TEXT    NOT NULL,
    description      TEXT    NOT NULL DEFAULT '',
    requested_scopes TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(requested_scopes)),
    code             TEXT    NOT NULL CHECK (length(code) = 6 AND code NOT GLOB '*[^0-9]*'),
    poll_secret_hash BLOB    NOT NULL,
    source_ip        TEXT    NOT NULL DEFAULT '',
    state            TEXT    NOT NULL DEFAULT 'pending'
                     CHECK (state IN ('pending', 'approved', 'denied', 'expired', 'claimed', 'cancelled')),
    approved_app_id  TEXT    REFERENCES apps (id) ON DELETE SET NULL,
    token_ciphertext BLOB,
    created_at       INTEGER NOT NULL,
    expires_at       INTEGER NOT NULL,
    replaces_app_id  TEXT    REFERENCES apps (id) ON DELETE SET NULL,
    replaced         INTEGER NOT NULL DEFAULT 0 CHECK (replaced IN (0, 1))
) STRICT;

INSERT INTO pairing_requests_new (id, app_name, description, requested_scopes, code, poll_secret_hash, source_ip,
    state, approved_app_id, token_ciphertext, created_at, expires_at)
SELECT id, app_name, description, requested_scopes, code, poll_secret_hash, source_ip,
    state, approved_app_id, token_ciphertext, created_at, expires_at
FROM pairing_requests;

DROP TABLE pairing_requests;

ALTER TABLE pairing_requests_new RENAME TO pairing_requests;

CREATE UNIQUE INDEX pairing_requests_pending_code ON pairing_requests (code) WHERE state = 'pending';
