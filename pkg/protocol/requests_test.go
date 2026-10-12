package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPatchDistinguishesAbsentNullAndValue(t *testing.T) {
	tests := []struct {
		name, in  string
		wantSet   bool
		wantValue *int
	}{
		{"absent", `{}`, false, nil},
		{"null", `{"retention_days":null}`, true, nil},
		{"value", `{"retention_days":30}`, true, new(30)},
	}
	for _, tc := range tests {
		var req UpdateChannelRequest
		if err := json.Unmarshal([]byte(tc.in), &req); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := req.RetentionDays
		if got.Set != tc.wantSet || (got.Value == nil) != (tc.wantValue == nil) || (got.Value != nil && *got.Value != *tc.wantValue) {
			t.Errorf("%s: got %+v, want set=%v value=%v", tc.name, got, tc.wantSet, tc.wantValue)
		}
	}
	var bad UpdateChannelRequest
	if err := json.Unmarshal([]byte(`{"retention_days":"soon"}`), &bad); err == nil {
		t.Error("a string retention_days was accepted")
	}
}

func TestPatchMarshalsOnlyWhenSet(t *testing.T) {
	raw, err := json.Marshal(UpdateMembershipRequest{})
	if err != nil || string(raw) != `{}` {
		t.Errorf("unset patch = %s, %v", raw, err)
	}
	until := "2030-01-02T03:04:05Z"
	raw, err = json.Marshal(UpdateMembershipRequest{MutedUntil: Patch[string]{Set: true, Value: &until}})
	if err != nil || string(raw) != `{"muted_until":"2030-01-02T03:04:05Z"}` {
		t.Errorf("set patch = %s, %v", raw, err)
	}
	raw, err = json.Marshal(UpdateMembershipRequest{MutedUntil: Patch[string]{Set: true}})
	if err != nil || string(raw) != `{"muted_until":null}` {
		t.Errorf("clear patch = %s, %v", raw, err)
	}
}

func TestUpdateMeRequestPatches(t *testing.T) {
	var req UpdateMeRequest
	in := `{"display_name":"Ann","status_text":null,"avatar_file_id":null,"prefs":{"theme":"dark"}}`
	if err := json.Unmarshal([]byte(in), &req); err != nil {
		t.Fatal(err)
	}
	if !req.DisplayName.Set || *req.DisplayName.Value != "Ann" {
		t.Errorf("display_name = %+v", req.DisplayName)
	}
	if !req.StatusText.Set || req.StatusText.Value != nil || !req.AvatarFileID.Set || req.AvatarFileID.Value != nil {
		t.Errorf("null fields = %+v %+v", req.StatusText, req.AvatarFileID)
	}
	if !req.Prefs.Set || string(*req.Prefs.Value) != `{"theme":"dark"}` {
		t.Errorf("prefs = %+v", req.Prefs)
	}
	var empty UpdateMeRequest
	if err := json.Unmarshal([]byte(`{}`), &empty); err != nil || empty.DisplayName.Set || empty.Prefs.Set {
		t.Errorf("empty = %+v, %v", empty, err)
	}
	raw, err := json.Marshal(empty)
	if err != nil || string(raw) != `{}` {
		t.Errorf("marshal of an empty request = %s, %v", raw, err)
	}
}

func TestFileJSONAndURLs(t *testing.T) {
	w, h, thumb := 640, 480, FileThumbURL("01ARZ3NDEKTSV4RRFFQ69G5FAZ", "a b.png")
	raw, err := json.Marshal(File{
		ID: "01ARZ3NDEKTSV4RRFFQ69G5FAZ", Name: "a b.png", Size: 12, Mime: "image/png", Category: FileImage,
		Width: &w, Height: &h, HasThumb: true, MetaStripped: true, URL: FileURL("01ARZ3NDEKTSV4RRFFQ69G5FAZ", "a b.png"), ThumbURL: &thumb,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"01ARZ3NDEKTSV4RRFFQ69G5FAZ","name":"a b.png","size":12,"mime":"image/png","category":"image","width":640,"height":480,` +
		`"has_thumb":true,"meta_stripped":true,"url":"/api/v1/files/01ARZ3NDEKTSV4RRFFQ69G5FAZ/a%20b.png","thumb_url":"/api/v1/files/01ARZ3NDEKTSV4RRFFQ69G5FAZ/a%20b.png?thumb=1"}`
	if string(raw) != want {
		t.Errorf("File JSON =\n%s\nwant\n%s", raw, want)
	}
	if got := AvatarURL("01ARZ3NDEKTSV4RRFFQ69G5FAZ"); got != "/api/v1/files/01ARZ3NDEKTSV4RRFFQ69G5FAZ/avatar" {
		t.Errorf("AvatarURL = %s", got)
	}
	raw, _ = json.Marshal(File{})
	if !strings.Contains(string(raw), `"width":null`) || !strings.Contains(string(raw), `"thumb_url":null`) {
		t.Errorf("zero File = %s", raw)
	}
}
