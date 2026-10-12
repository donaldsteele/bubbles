package reset

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
)

// snapshotSettings reads the settings rows a factory reset with KeepSettings
// carries over: known setting keys with valid JSON only. Identity and
// secrets (instance id, VAPID keys, CA, tokens) never live in the settings
// table, so they can never be carried.
func snapshotSettings(ctx context.Context, db *store.DB) (map[string]json.RawMessage, error) {
	rows, err := db.Read().QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("snapshot settings: %w", err)
	}
	defer rows.Close()
	known := settings.Keys()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("snapshot settings: %w", err)
		}
		if slices.Contains(known, key) && json.Valid([]byte(value)) {
			out[key] = json.RawMessage(value)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("snapshot settings: %w", err)
	}
	return out, nil
}

// restoreSettings writes the snapshot into a fresh database in one
// transaction, skipping anything that is not a known setting. It writes no
// audit rows: the factory-reset row is the first one.
func restoreSettings(ctx context.Context, db *store.DB, snap map[string]json.RawMessage) error {
	known := settings.Keys()
	return db.Do(ctx, func(tx *store.Tx) error {
		for key, value := range snap {
			if !slices.Contains(known, key) || !json.Valid(value) {
				continue
			}
			if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
				ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, string(value)); err != nil {
				return fmt.Errorf("restore setting: %w", err)
			}
		}
		return nil
	})
}
