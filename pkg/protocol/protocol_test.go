package protocol

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFormatTime(t *testing.T) {
	ms := time.Date(2026, 3, 1, 12, 30, 45, 123_000_000, time.UTC).UnixMilli()
	if got, want := FormatTime(ms), "2026-03-01T12:30:45.123Z"; got != want {
		t.Errorf("FormatTime = %q, want %q", got, want)
	}
	if got, want := FormatTime(0), "1970-01-01T00:00:00.000Z"; got != want {
		t.Errorf("FormatTime(0) = %q, want %q", got, want)
	}
	if _, err := time.Parse(time.RFC3339Nano, FormatTime(ms)); err != nil {
		t.Errorf("not RFC 3339: %v", err)
	}
}

func TestErrorEnvelope(t *testing.T) {
	in := ErrorBody{Error: Error{Code: CodeBadRequest, Message: "bad", Details: map[string]any{DetailsFields: map[string]string{"password": "too short"}}}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"error":{"code":"bad_request","message":"bad","details":{"fields":{"password":"too short"}}}}`
	if string(raw) != want {
		t.Errorf("json = %s\nwant   %s", raw, want)
	}
	var out ErrorBody
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.Error.FieldErrors(); got["password"] != "too short" || len(got) != 1 {
		t.Errorf("FieldErrors = %v", got)
	}
	var plain Error
	if plain.FieldErrors() != nil {
		t.Error("FieldErrors on an error without details")
	}
	raw, _ = json.Marshal(ErrorBody{Error: Error{Code: CodeInternal, Message: "x"}})
	if strings.Contains(string(raw), "details") {
		t.Errorf("empty details serialized: %s", raw)
	}
	var asErr error = &out.Error
	var target *Error
	if !errors.As(asErr, &target) || !strings.Contains(asErr.Error(), "bad_request") {
		t.Errorf("Error() = %q", asErr.Error())
	}
}

func TestMeFlattensUser(t *testing.T) {
	me := Me{
		User:       User{ID: "u1", Username: "alice", Kind: KindHuman, Role: RoleMember, CreatedAt: "2026-03-01T12:00:00.000Z"},
		Prefs:      json.RawMessage(`{"theme":"dark"}`),
		MustChange: true,
	}
	raw, err := json.Marshal(me)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "username", "display_name", "kind", "role", "avatar_file_id", "status_text", "deactivated", "created_at", "prefs", "must_change_pw"} {
		if _, ok := m[k]; !ok {
			t.Errorf("key %q missing from %s", k, raw)
		}
	}
	if m["avatar_file_id"] != nil || m["must_change_pw"] != true {
		t.Errorf("values = %v", m)
	}
}
