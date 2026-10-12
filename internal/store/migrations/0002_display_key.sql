-- People sign in and are addressed by a free-form display name. display_key
-- is its case- and width-folded form, used for uniqueness among active
-- humans and for login. It is NULL for apps, and for humans until the auth
-- service backfills it; it is not UNIQUE because data from before this
-- migration may hold duplicates.
ALTER TABLE users ADD COLUMN display_key TEXT;
CREATE INDEX users_display_key ON users(display_key);
