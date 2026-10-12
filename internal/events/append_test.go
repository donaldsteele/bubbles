package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/store"
)

var testNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName),
		store.Options{Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestAppend(t *testing.T) {
	db := openDB(t)
	in := []Event{
		{Type: TypeMessageCreated, ChannelID: "c1", Payload: json.RawMessage(`{"id":"m1"}`)},
		{Type: TypeReadUpdated, UserID: "u1", CreatedAt: 42},
		{Type: TypeSettingsUpdated},
	}
	var seqs []int64
	err := db.Do(context.Background(), func(tx *store.Tx) error {
		for _, e := range in {
			seq, err := Append(tx, e)
			if err != nil {
				return err
			}
			seqs = append(seqs, seq)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	want := []struct {
		channel, user sql.NullString
		payload       string
		createdAt     int64
	}{
		{sql.NullString{String: "c1", Valid: true}, sql.NullString{}, `{"id":"m1"}`, testNow.UnixMilli()},
		{sql.NullString{}, sql.NullString{String: "u1", Valid: true}, `{}`, 42},
		{sql.NullString{}, sql.NullString{}, `{}`, testNow.UnixMilli()},
	}
	for i, seq := range seqs {
		if i > 0 && seq <= seqs[i-1] {
			t.Errorf("seq %d = %d, not above %d", i, seq, seqs[i-1])
		}
		var typ, payload string
		var channel, user sql.NullString
		var createdAt int64
		err := db.Read().QueryRow(`SELECT type, channel_id, user_id, payload, created_at FROM events WHERE seq = ?`, seq).
			Scan(&typ, &channel, &user, &payload, &createdAt)
		if err != nil {
			t.Fatalf("event %d: %v", seq, err)
		}
		w := want[i]
		if typ != in[i].Type || channel != w.channel || user != w.user || payload != w.payload || createdAt != w.createdAt {
			t.Errorf("event %d = (%s,%v,%v,%s,%d), want (%s,%v,%v,%s,%d)", i,
				typ, channel, user, payload, createdAt, in[i].Type, w.channel, w.user, w.payload, w.createdAt)
		}
	}
}

func TestAppendRejects(t *testing.T) {
	db := openDB(t)
	tests := []struct {
		name string
		e    Event
	}{
		{"empty type", Event{}},
		{"unknown type", Event{Type: "message.exploded"}},
		{"invalid payload", Event{Type: TypeMessageCreated, Payload: json.RawMessage(`{"a":`)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := db.Do(context.Background(), func(tx *store.Tx) error {
				_, err := Append(tx, tc.e)
				return err
			})
			if err == nil {
				t.Error("Append accepted the event, want error")
			}
		})
	}
	var n int
	if err := db.Read().QueryRow(`SELECT count(*) FROM events`).Scan(&n); err != nil || n != 0 {
		t.Errorf("events rows = %d (err %v), want 0", n, err)
	}
}

func TestAppendRejectsEphemeralTypes(t *testing.T) {
	db := openDB(t)
	for _, typ := range []string{TypeTyping, TypePresenceChanged, TypeEphemeralMessage} {
		t.Run(typ, func(t *testing.T) {
			if !IsEphemeral(typ) {
				t.Fatalf("IsEphemeral(%q) = false", typ)
			}
			err := db.Do(context.Background(), func(tx *store.Tx) error {
				_, err := Append(tx, Event{Type: typ, ChannelID: "c1"})
				return err
			})
			if !errors.Is(err, ErrEphemeral) {
				t.Errorf("Append = %v, want ErrEphemeral", err)
			}
		})
	}
	if IsEphemeral(TypeMessageCreated) {
		t.Error("IsEphemeral(message.created) = true")
	}
	var n int
	if err := db.Read().QueryRow(`SELECT count(*) FROM events`).Scan(&n); err != nil || n != 0 {
		t.Errorf("events rows = %d (err %v), want 0", n, err)
	}
}

func TestAppendRolledBackWithTransaction(t *testing.T) {
	db := openDB(t)
	errAbort := errors.New("abort")
	err := db.Do(context.Background(), func(tx *store.Tx) error {
		if _, err := Append(tx, Event{Type: TypeMessageCreated, ChannelID: "c1"}); err != nil {
			return err
		}
		return errAbort
	})
	if !errors.Is(err, errAbort) {
		t.Fatalf("Do = %v, want abort", err)
	}
	var n int
	if err := db.Read().QueryRow(`SELECT count(*) FROM events`).Scan(&n); err != nil || n != 0 {
		t.Errorf("events rows = %d (err %v), want 0", n, err)
	}
}

func TestTypesAndCloseCodes(t *testing.T) {
	seen := map[string]bool{}
	for _, typ := range Types {
		if seen[typ] {
			t.Errorf("duplicate type %q", typ)
		}
		seen[typ] = true
		if !Known(typ) {
			t.Errorf("Known(%q) = false", typ)
		}
	}
	if len(Types) != 23 {
		t.Errorf("len(Types) = %d, want 23", len(Types))
	}
	codes := []struct {
		code   int
		want   int
		reason string
	}{
		{CloseSlowConsumer, 4008, ReasonSlowConsumer},
		{CloseSessionRevoked, 4009, ReasonSessionRevoked},
		{CloseServerReset, 4010, ReasonServerReset},
		{CloseContentCleared, 4011, ReasonContentCleared},
	}
	for _, c := range codes {
		if c.code != c.want || c.reason == "" {
			t.Errorf("close code %d (%q), want %d", c.code, c.reason, c.want)
		}
	}
}
