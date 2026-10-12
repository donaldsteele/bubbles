package settings

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/store"
)

// SetMany validates every entry first and then stores all of them in one
// transaction, so a rejected value changes nothing. Each key is audited.
func (s *Store) SetMany(ctx context.Context, actor audit.Actor, values map[string]json.RawMessage) error {
	keys := make([]string, 0, len(values))
	for k, raw := range values {
		if err := Validate(k, raw); err != nil {
			return err
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return s.db.Do(ctx, func(tx *store.Tx) error {
		for _, k := range keys {
			if err := s.SetTx(tx, actor, k, values[k]); err != nil {
				return err
			}
		}
		return nil
	})
}
