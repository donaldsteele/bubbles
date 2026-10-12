package audit

import (
	"context"
	"database/sql"
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

type row struct {
	id, kind, ip, action, targetType, targetID, detail string
	actorID                                            sql.NullString
	at                                                 int64
}

func rows(t *testing.T, db *store.DB) []row {
	t.Helper()
	rs, err := db.Read().Query(`SELECT id, at, actor_id, actor_kind, actor_ip, action, target_type, target_id, detail FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []row
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.id, &r.at, &r.actorID, &r.kind, &r.ip, &r.action, &r.targetType, &r.targetID, &r.detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestWrite(t *testing.T) {
	db := openDB(t)
	err := db.Do(context.Background(), func(tx *store.Tx) error {
		if _, err := Write(tx, User("u1", "192.168.1.5").Entry(ActionLoginSuccess, "session", "s1", map[string]any{"k": "v"})); err != nil {
			return err
		}
		if _, err := Write(tx, Console.Entry(ActionInviteCreate, "invite", "i1", nil)); err != nil {
			return err
		}
		_, err := Write(tx, Entry{ActorKind: KindSystem, Action: "custom.action"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got := rows(t, db)
	if len(got) != 3 {
		t.Fatalf("%d rows", len(got))
	}
	r := got[0]
	if r.actorID.String != "u1" || r.kind != KindUser || r.ip != "192.168.1.5" || r.action != ActionLoginSuccess ||
		r.targetType != "session" || r.targetID != "s1" || r.detail != `{"k":"v"}` || r.at != testNow.UnixMilli() {
		t.Errorf("row 0 = %+v", r)
	}
	if r := got[1]; r.actorID.Valid || r.kind != KindConsole || r.detail != "{}" {
		t.Errorf("console row = %+v (actor_id must be NULL, detail {})", r)
	}
	if r := got[2]; r.actorID.Valid || r.kind != KindSystem || r.targetType != "" || r.detail != "{}" {
		t.Errorf("system row = %+v", r)
	}
	if !(got[0].id < got[1].id && got[1].id < got[2].id) {
		t.Error("ids are not increasing")
	}
}

func TestWriteIsPartOfTheTransaction(t *testing.T) {
	db := openDB(t)
	boom := errors.New("boom")
	err := db.Do(context.Background(), func(tx *store.Tx) error {
		if _, err := Write(tx, System.Entry(ActionSetup, "", "", nil)); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if n := len(rows(t, db)); n != 0 {
		t.Errorf("%d audit rows survived a rolled-back change", n)
	}
}

func TestWriteValidation(t *testing.T) {
	db := openDB(t)
	for name, e := range map[string]Entry{
		"no action":     {ActorKind: KindSystem},
		"no actor kind": {Action: "x"},
		"bad detail":    {ActorKind: KindSystem, Action: "x", Detail: make(chan int)},
	} {
		err := db.Do(context.Background(), func(tx *store.Tx) error {
			_, err := Write(tx, e)
			return err
		})
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if n := len(rows(t, db)); n != 0 {
		t.Errorf("%d rows written by invalid entries", n)
	}
}
