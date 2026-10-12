-- Self sign-up became open by default in v0.2.0, but only for new servers:
-- a server upgraded from v0.1.x was invite-only (open_registration was off
-- by default), so it stays that way unless an admin opens sign-up. A new or
-- factory-reset server has no users when this runs, so nothing is written.
INSERT INTO settings (key, value)
SELECT 'open_registration', 'false'
WHERE EXISTS (SELECT 1 FROM users)
  AND NOT EXISTS (SELECT 1 FROM settings WHERE key = 'open_registration');
