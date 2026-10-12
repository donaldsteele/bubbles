package store

import (
	"context"
	"testing"
)

type valueKey struct{}

func TestDBValuesVisibleFromTx(t *testing.T) {
	db := openTestDB(t, Options{})
	if got := db.Value(valueKey{}); got != nil {
		t.Fatalf("Value before SetValue = %v, want nil", got)
	}
	db.SetValue(valueKey{}, "hub")
	var seen any
	if err := db.Do(context.Background(), func(tx *Tx) error {
		seen = tx.Value(valueKey{})
		return nil
	}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if seen != "hub" {
		t.Fatalf("tx.Value = %v, want hub", seen)
	}
	db.SetValue(valueKey{}, nil)
	if got := db.Value(valueKey{}); got != nil {
		t.Fatalf("Value after reset = %v, want nil", got)
	}
}
