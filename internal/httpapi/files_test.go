package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/files"
	"github.com/donaldsteele/bubbles/internal/files/filestest"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// multipartBody builds a multipart/form-data body with one part.
func multipartBody(t *testing.T, field, filename string, data []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="`+field+`"; filename="`+filename+`"`)
	h.Set("Content-Type", "application/octet-stream")
	w, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

func (m member) tryUpload(t *testing.T, name string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := multipartBody(t, "file", name, data)
	return m.do(http.MethodPost, "/api/v1/files", body, func(r *http.Request) { r.Header.Set("Content-Type", ct) })
}

func (m member) upload(t *testing.T, name string, data []byte) protocol.File {
	t.Helper()
	rec := m.tryUpload(t, name, data)
	wantStatus(t, rec, http.StatusCreated, "")
	return decodeBody[protocol.File](t, rec)
}

func (m member) get(path string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	return m.do(http.MethodGet, path, nil, mods...)
}

func (m member) postWithFiles(t *testing.T, channelID, body string, fileIDs ...string) *httptest.ResponseRecorder {
	t.Helper()
	return m.do(http.MethodPost, "/api/v1/channels/"+channelID+"/messages", protocol.CreateMessageRequest{Body: body, FileIDs: fileIDs})
}

// fileSHA is the sha256 recorded for a file.
func (e *env) fileSHA(id string) string {
	e.t.Helper()
	var sha string
	if err := e.db.Read().QueryRow(`SELECT sha256 FROM files WHERE id = ?`, id).Scan(&sha); err != nil {
		e.t.Fatal(err)
	}
	return sha
}

func (e *env) set(t *testing.T, key string, value any) {
	t.Helper()
	if err := e.st.Set(e.ctx, audit.System, key, value); err != nil {
		t.Fatal(err)
	}
}

func TestFilesUploadAndServeAnImage(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()
	src := filestest.JPEGWithMeta(800, 600, 1)

	f := alice.upload(t, "Holiday 2026 – Côte d'Azur.jpg", src)
	if f.Category != protocol.FileImage || f.Mime != "image/jpeg" || !f.MetaStripped || !f.HasThumb || f.ThumbURL == nil {
		t.Fatalf("file = %+v", f)
	}
	if f.Width == nil || *f.Width != 800 || *f.Height != 600 {
		t.Errorf("size = %v x %v", f.Width, f.Height)
	}
	if f.URL != protocol.FileURL(f.ID, f.Name) || *f.ThumbURL != protocol.FileThumbURL(f.ID, f.Name) {
		t.Errorf("urls = %s, %s", f.URL, *f.ThumbURL)
	}

	rec := alice.get(f.URL)
	wantStatus(t, rec, http.StatusOK, "")
	h := rec.Header()
	for name, want := range map[string]string{
		"Content-Type":                 "image/jpeg",
		"X-Content-Type-Options":       "nosniff",
		"Content-Security-Policy":      "sandbox; default-src 'none'; img-src 'self'; media-src 'self'",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Cache-Control":                "private, max-age=31536000, immutable",
		"ETag":                         `"` + e.fileSHA(f.ID) + `"`,
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	disp := h.Get("Content-Disposition")
	if !strings.HasPrefix(disp, "inline; filename=") || !strings.Contains(disp, "filename*=UTF-8''Holiday%202026%20%E2%80%93%20C%C3%B4te%20d%27Azur.jpg") {
		t.Errorf("Content-Disposition = %q", disp)
	}
	if sum := sha256.Sum256(rec.Body.Bytes()); hex.EncodeToString(sum[:]) != e.fileSHA(f.ID) {
		t.Error("the ETag is not the SHA-256 of the served bytes")
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("Exif")) || bytes.Contains(rec.Body.Bytes(), []byte(filestest.SecretXMP)) {
		t.Error("the served image still has its metadata")
	}
	if _, err := jpeg.Decode(bytes.NewReader(rec.Body.Bytes())); err != nil {
		t.Errorf("the served image does not decode: %v", err)
	}

	// Revalidation and ranges.
	wantStatus(t, alice.get(f.URL, func(r *http.Request) { r.Header.Set("If-None-Match", h.Get("ETag")) }), http.StatusNotModified, "")
	part := alice.get(f.URL, func(r *http.Request) { r.Header.Set("Range", "bytes=0-9") })
	wantStatus(t, part, http.StatusPartialContent, "")
	if part.Body.Len() != 10 || !strings.HasPrefix(part.Header().Get("Content-Range"), "bytes 0-9/") || part.Header().Get("Accept-Ranges") != "bytes" {
		t.Errorf("range answer: %d bytes, Content-Range %q", part.Body.Len(), part.Header().Get("Content-Range"))
	}
	wantStatus(t, alice.get(f.URL, func(r *http.Request) { r.Header.Set("Range", "bytes=99999999-") }), http.StatusRequestedRangeNotSatisfiable, "")

	// The thumbnail and the forced download.
	th := alice.get(*f.ThumbURL)
	wantStatus(t, th, http.StatusOK, "")
	if cfg, _, err := image.DecodeConfig(th.Body); err != nil || cfg.Width != 640 || cfg.Height != 480 {
		t.Errorf("thumbnail = %+v, %v", cfg, err)
	}
	if th.Header().Get("Content-Type") != "image/jpeg" || th.Header().Get("ETag") == h.Get("ETag") {
		t.Errorf("thumbnail headers: %v", th.Header())
	}
	dl := alice.get(f.URL + "?download=1")
	wantStatus(t, dl, http.StatusOK, "")
	if !strings.HasPrefix(dl.Header().Get("Content-Disposition"), "attachment;") || dl.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("download headers: %v", dl.Header())
	}

	// A file without a thumbnail has no thumbnail URL.
	small := alice.upload(t, "small.png", filestest.PlainPNG(20, 20))
	if small.HasThumb {
		t.Fatal("a 20 px image got a thumbnail")
	}
	wantStatus(t, alice.get(protocol.FileURL(small.ID, "small.png")+"?thumb=1"), http.StatusNotFound, CodeNotFound)
}

func TestFilesSVGIsServedAsAnAttachmentWithSandboxCSP(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()
	src := filestest.SVGWithScript()
	f := alice.upload(t, "logo.svg", src)
	if f.Category != protocol.FileOther || f.Mime != "image/svg+xml" || f.MetaStripped || f.HasThumb {
		t.Fatalf("file = %+v", f)
	}
	rec := alice.get(f.URL)
	wantStatus(t, rec, http.StatusOK, "")
	h := rec.Header()
	if h.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(h.Get("Content-Disposition"), `attachment; filename="logo.svg"`) {
		t.Errorf("Content-Type %q, Content-Disposition %q", h.Get("Content-Type"), h.Get("Content-Disposition"))
	}
	if h.Get("Content-Security-Policy") != "sandbox; default-src 'none'; img-src 'self'; media-src 'self'" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Errorf("security headers: %v", h)
	}
	if !bytes.Equal(rec.Body.Bytes(), src) {
		t.Error("an SVG download must be the uploaded bytes")
	}
	// Whatever the query asks, a plain file never becomes inline.
	rec = alice.get(f.URL + "?thumb=0&download=0")
	if rec.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment;") {
		t.Errorf("headers = %v", rec.Header())
	}
}

func TestFilesAreVisibleOnlyToTheirAudience(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()
	bob := e.newMember("bob", "192.168.1.51")
	carol := e.newMember("carol", "192.168.1.52")
	guest := e.newGuest("gina", "192.168.1.53")
	private := alice.createChannel(t, "secret", protocol.ChannelPrivate, bob)

	f := alice.upload(t, "plan.png", filestest.PlainPNG(30, 30))
	// Before it is attached only the uploader sees the file.
	wantStatus(t, alice.get(f.URL), http.StatusOK, "")
	for _, m := range []member{bob, carol, guest} {
		wantStatus(t, m.get(f.URL), http.StatusNotFound, CodeNotFound)
	}

	msg := decodeBody[protocol.Message](t, alice.postWithFiles(t, private.ID, "the plan", f.ID))
	if len(msg.Files) != 1 || msg.Files[0].ID != f.ID {
		t.Fatalf("message files = %+v", msg.Files)
	}
	// Members of the private channel see it, outsiders (admin-level members and guests) do not.
	wantStatus(t, bob.get(f.URL), http.StatusOK, "")
	wantStatus(t, bob.get(f.URL+"?download=1"), http.StatusOK, "")
	wantStatus(t, carol.get(f.URL), http.StatusNotFound, CodeNotFound)
	wantStatus(t, guest.get(f.URL), http.StatusNotFound, CodeNotFound)
	// A bogus id is the same 404.
	wantStatus(t, bob.get(protocol.FileURL("01ARZ3NDEKTSV4RRFFQ69G5FAZ", "x.png")), http.StatusNotFound, CodeNotFound)
	wantStatus(t, e.client("").do(http.MethodGet, f.URL, nil), http.StatusUnauthorized, CodeUnauthorized)

	// Once the member who is not the uploader leaves, access ends too.
	wantStatus(t, bob.do(http.MethodPost, "/api/v1/channels/"+private.ID+"/leave", nil), http.StatusNoContent, "")
	wantStatus(t, bob.get(f.URL), http.StatusNotFound, CodeNotFound)

	// A file in a public channel is visible to every member, not to guests who did not join.
	pub := alice.createChannel(t, "town-hall", protocol.ChannelPublic)
	g := alice.upload(t, "agenda.png", filestest.PlainPNG(30, 30))
	wantStatus(t, alice.postWithFiles(t, pub.ID, "agenda", g.ID), http.StatusCreated, "")
	wantStatus(t, carol.get(g.URL), http.StatusOK, "")
	wantStatus(t, guest.get(g.URL), http.StatusNotFound, CodeNotFound)

	// Deleting the message withdraws the file from everybody but its uploader.
	gm := decodeBody[protocol.MessagePage](t, alice.get("/api/v1/channels/"+pub.ID+"/messages")).Items[0]
	wantStatus(t, alice.do(http.MethodDelete, "/api/v1/messages/"+gm.ID, nil), http.StatusNoContent, "")
	wantStatus(t, carol.get(g.URL), http.StatusNotFound, CodeNotFound)
	wantStatus(t, alice.get(g.URL), http.StatusOK, "")
}

// TestFilesAppsNeedMessagesReadToDownload: file bytes need messages:read for an
// app; channels:read shows the channel and its file list, not the content.
func TestFilesAppsNeedMessagesReadToDownload(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	town := admin.createChannel(t, "town-hall", protocol.ChannelPublic)
	f := admin.upload(t, "agenda.png", filestest.PlainPNG(30, 30))
	wantStatus(t, admin.postWithFiles(t, town.ID, "agenda", f.ID), http.StatusCreated, "")

	channelsOnly := e.newBot(admin, "channels-only", []string{"channels:read"}, nil, town.ID)
	wantStatus(t, channelsOnly.do(e, http.MethodGet, f.URL, nil), http.StatusNotFound, CodeNotFound)
	wantStatus(t, channelsOnly.do(e, http.MethodGet, f.URL+"?download=1", nil), http.StatusNotFound, CodeNotFound)
	reader := e.newBot(admin, "reader", []string{"messages:read"}, nil, town.ID)
	wantStatus(t, reader.do(e, http.MethodGet, f.URL, nil), http.StatusOK, "")
}

func TestFilesAvatarsAreVisibleToEverySignedInUser(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()
	guest := e.newGuest("gina", "192.168.1.53")
	f := alice.upload(t, "me.png", filestest.PlainPNG(40, 40))
	wantStatus(t, guest.get(f.URL), http.StatusNotFound, CodeNotFound)

	rec := alice.do(http.MethodPatch, "/api/v1/me", protocol.UpdateMeRequest{AvatarFileID: protocol.Patch[string]{Set: true, Value: &f.ID}})
	wantStatus(t, rec, http.StatusOK, "")
	wantStatus(t, guest.get(protocol.AvatarURL(f.ID)), http.StatusOK, "")
	wantStatus(t, e.newMember("bob", "192.168.1.51").get(f.URL), http.StatusOK, "")

	rec = alice.do(http.MethodPatch, "/api/v1/me", `{"avatar_file_id":null}`)
	wantStatus(t, rec, http.StatusOK, "")
	wantStatus(t, guest.get(f.URL), http.StatusNotFound, CodeNotFound)
}

func TestFilesAttachRules(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()
	bob := e.newMember("bob", "192.168.1.51")
	general := e.generalID()

	a := alice.upload(t, "a.png", filestest.PlainPNG(10, 10))
	b := alice.upload(t, "b.png", filestest.PlainPNG(12, 12))
	other := bob.upload(t, "bobs.png", filestest.PlainPNG(14, 14))

	// Somebody else's file, an unknown id, and an empty list of known ids all fail the same way.
	for name, ids := range map[string][]string{
		"foreign":    {other.ID},
		"unknown":    {"01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		"mixed":      {a.ID, other.ID},
		"duplicates": {a.ID, a.ID},
	} {
		rec := alice.postWithFiles(t, general, "x", ids...)
		wantStatus(t, rec, http.StatusBadRequest, CodeBadRequest)
		if fieldErrors(decodeError(t, rec))["file_ids"] == "" {
			t.Errorf("%s: no details.fields.file_ids", name)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM attachments`); n != 0 {
		t.Errorf("%d attachments after failed posts", n)
	}
	var many []string
	for range 11 {
		many = append(many, a.ID)
	}
	wantStatus(t, alice.postWithFiles(t, general, "x", many...), http.StatusBadRequest, CodeBadRequest)

	// Order is kept, and a file without text is fine.
	rec := alice.postWithFiles(t, general, "", b.ID, a.ID)
	wantStatus(t, rec, http.StatusCreated, "")
	msg := decodeBody[protocol.Message](t, rec)
	if len(msg.Files) != 2 || msg.Files[0].ID != b.ID || msg.Files[1].ID != a.ID {
		t.Fatalf("files = %+v, want [b a]", msg.Files)
	}
	page := decodeBody[protocol.MessagePage](t, alice.get("/api/v1/channels/"+general+"/messages"))
	if got := page.Items[len(page.Items)-1].Files; len(got) != 2 || got[0].ID != b.ID || got[1].ID != a.ID {
		t.Errorf("listed files = %+v, want [b a]", got)
	}

	// A file belongs to one message only.
	wantStatus(t, alice.postWithFiles(t, general, "again", a.ID), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, bob.postWithFiles(t, general, "mine", other.ID), http.StatusCreated, "")
}

func TestFilesUploadLimits(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()
	data := filestest.PlainPNG(30, 30)
	n := int64(len(data))

	// upload_max_bytes
	e.set(t, "upload_max_bytes", n-1)
	rec := alice.tryUpload(t, "a.png", data)
	if err := wantStatus(t, rec, http.StatusRequestEntityTooLarge, CodeTooLarge); err.Details["limit"] != "upload_max_bytes" || err.Details["max_bytes"] != float64(n-1) {
		t.Errorf("details = %v", err.Details)
	}
	// A request far beyond the limit is refused from its Content-Length.
	rec = alice.tryUpload(t, "big.bin", bytes.Repeat([]byte{1}, 2<<20))
	wantStatus(t, rec, http.StatusRequestEntityTooLarge, CodeTooLarge)
	e.set(t, "upload_max_bytes", 100<<20)

	// user_quota_bytes
	e.set(t, "user_quota_bytes", n*2+n/2)
	alice.upload(t, "1.png", data)
	alice.upload(t, "2.png", data)
	rec = alice.tryUpload(t, "3.png", data)
	if err := wantStatus(t, rec, http.StatusRequestEntityTooLarge, CodeTooLarge); err.Details["limit"] != "user_quota_bytes" || err.Details["used_bytes"] != float64(2*n) {
		t.Errorf("quota details = %v", err.Details)
	}
	e.set(t, "user_quota_bytes", 0)
	alice.upload(t, "3.png", data)

	// disk_reserve_bytes
	e.free.Store(100 << 20) // the default reserve is 1 GiB
	wantStatus(t, alice.tryUpload(t, "4.png", data), http.StatusInsufficientStorage, CodeInsufficientStorage)
	e.set(t, "disk_reserve_bytes", 10<<20)
	alice.upload(t, "4.png", data)

	if got := e.count(`SELECT COUNT(*) FROM files`); got != 4 {
		t.Errorf("%d files stored, want 4", got)
	}
}

func TestFilesUploadConcurrencyIsLimitedPerUser(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()
	bob := e.newMember("bob", "192.168.1.51")
	var releases []func()
	for range files.MaxConcurrentUploads {
		release, err := e.files.Begin(alice.id)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	rec := alice.tryUpload(t, "a.png", filestest.PlainPNG(10, 10))
	wantStatus(t, rec, http.StatusTooManyRequests, CodeRateLimited)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After header")
	}
	bob.upload(t, "b.png", filestest.PlainPNG(10, 10))
	releases[0]()
	alice.upload(t, "a.png", filestest.PlainPNG(10, 10))
	for _, r := range releases[1:] {
		r()
	}
}

func TestFilesUploadRejectsMalformedRequests(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()

	wantStatus(t, alice.do(http.MethodPost, "/api/v1/files", `{"file":"x"}`), http.StatusUnsupportedMediaType, CodeUnsupportedMedia)
	wantStatus(t, alice.do(http.MethodPost, "/api/v1/files", "x", func(r *http.Request) { r.Header.Set("Content-Type", "multipart/form-data") }),
		http.StatusUnsupportedMediaType, CodeUnsupportedMedia)

	// No part named file.
	body, ct := multipartBody(t, "attachment", "a.png", filestest.PlainPNG(10, 10))
	rec := alice.do(http.MethodPost, "/api/v1/files", body, func(r *http.Request) { r.Header.Set("Content-Type", ct) })
	if err := wantStatus(t, rec, http.StatusBadRequest, CodeBadRequest); fieldErrors(err)["file"] == "" {
		t.Errorf("details = %v", err.Details)
	}
	// Not multipart at all behind a multipart header.
	wantStatus(t, alice.do(http.MethodPost, "/api/v1/files", "garbage", func(r *http.Request) {
		r.Header.Set("Content-Type", "multipart/form-data; boundary=xyz")
	}), http.StatusBadRequest, CodeBadRequest)
	// A cross-origin upload is refused like any other state change.
	body, ct = multipartBody(t, "file", "a.png", filestest.PlainPNG(10, 10))
	wantStatus(t, alice.do(http.MethodPost, "/api/v1/files", body, func(r *http.Request) {
		r.Header.Set("Content-Type", ct)
		r.Header.Set("Origin", "https://evil.example")
	}), http.StatusForbidden, CodeForbidden)
	// Anonymous.
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/files", body, func(r *http.Request) { r.Header.Set("Content-Type", ct) }),
		http.StatusUnauthorized, CodeUnauthorized)
	if e.count(`SELECT COUNT(*) FROM files`) != 0 {
		t.Error("a rejected request stored a file")
	}
}

func TestFilesUploadStoresOnlyTheFilePartAndSanitizesTheName(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("note", "ignored")
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="..\..\evil\\..\"quoted\".txt"`)
	w, _ := mw.CreatePart(h)
	_, _ = w.Write([]byte("hello"))
	_ = mw.Close()
	rec := alice.do(http.MethodPost, "/api/v1/files", buf.Bytes(), func(r *http.Request) { r.Header.Set("Content-Type", mw.FormDataContentType()) })
	wantStatus(t, rec, http.StatusCreated, "")
	f := decodeBody[protocol.File](t, rec)
	if f.Name != `.."quoted".txt` || f.Size != 5 || f.Mime != "text/plain" {
		t.Errorf("file = %+v", f)
	}
}

func TestMePatchUpdatesTheProfileAndEmitsUserUpdated(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()
	patch := func(body any) *httptest.ResponseRecorder { return alice.do(http.MethodPatch, "/api/v1/me", body) }
	events := func() int { return e.count(`SELECT COUNT(*) FROM events WHERE type = 'user.updated'`) }

	rec := patch(`{"display_name":"  Alice A.  ","status_text":"on a call","prefs":{"theme":"dark","density":2}}`)
	wantStatus(t, rec, http.StatusOK, "")
	me := decodeBody[protocol.Me](t, rec)
	if me.DisplayName != "Alice A." || me.StatusText != "on a call" || string(me.Prefs) != `{"theme":"dark","density":2}` {
		t.Errorf("me = %+v", me)
	}
	if got := decodeBody[protocol.Me](t, alice.get("/api/v1/me")); got.DisplayName != "Alice A." || string(got.Prefs) != string(me.Prefs) {
		t.Errorf("GET /me = %+v", got)
	}
	if events() != 1 {
		t.Fatalf("%d user.updated events, want 1", events())
	}
	var payload string
	if err := e.db.Read().QueryRow(`SELECT payload FROM events WHERE type = 'user.updated'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"id":"`+me.ID+`"`) || !strings.Contains(payload, `"display_name":"Alice A."`) || strings.Contains(payload, "prefs") {
		t.Errorf("payload = %s", payload)
	}

	// Nothing changes, nothing is emitted; an empty body is a no-op too.
	wantStatus(t, patch(`{"display_name":"Alice A."}`), http.StatusOK, "")
	wantStatus(t, patch(`{}`), http.StatusOK, "")
	if events() != 1 {
		t.Errorf("%d user.updated events after no-op patches, want 1", events())
	}
	// Clearing and defaults.
	me = decodeBody[protocol.Me](t, patch(`{"status_text":null,"display_name":""}`))
	if me.StatusText != "" || me.DisplayName != "admin" {
		t.Errorf("me = %+v", me)
	}
	me = decodeBody[protocol.Me](t, patch(`{"prefs":{}}`))
	if string(me.Prefs) != `{}` || events() != 3 {
		t.Errorf("prefs %s, %d events", me.Prefs, events())
	}
}

func TestMePatchValidation(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()
	big := `{"prefs":{"k":"` + strings.Repeat("x", 4100) + `"}}`
	tests := []struct {
		name, body, field string
	}{
		{"null display name", `{"display_name":null}`, "display_name"},
		{"long display name", `{"display_name":"` + strings.Repeat("n", 65) + `"}`, "display_name"},
		{"control characters", `{"display_name":"a\u0007b"}`, "display_name"},
		{"long status", `{"status_text":"` + strings.Repeat("s", 101) + `"}`, "status_text"},
		{"null prefs", `{"prefs":null}`, "prefs"},
		{"prefs array", `{"prefs":[1,2]}`, "prefs"},
		{"prefs scalar", `{"prefs":"dark"}`, "prefs"},
		{"prefs over 4 KiB", big, "prefs"},
		{"unknown avatar", `{"avatar_file_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV"}`, "avatar_file_id"},
	}
	for _, tc := range tests {
		rec := alice.do(http.MethodPatch, "/api/v1/me", tc.body)
		if err := wantStatus(t, rec, http.StatusBadRequest, CodeBadRequest); fieldErrors(err)[tc.field] == "" {
			t.Errorf("%s: details = %v, want fields.%s", tc.name, err.Details, tc.field)
		}
	}
	wantStatus(t, alice.do(http.MethodPatch, "/api/v1/me", `{"username":"x"}`), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, alice.do(http.MethodPatch, "/api/v1/me", `{"display_name":"x"}`, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }),
		http.StatusUnsupportedMediaType, CodeUnsupportedMedia)
	wantStatus(t, e.client("").do(http.MethodPatch, "/api/v1/me", `{}`), http.StatusUnauthorized, CodeUnauthorized)
	if got := decodeBody[protocol.Me](t, alice.get("/api/v1/me")); got.DisplayName != "Admin" {
		t.Errorf("a rejected patch changed the profile: %+v", got)
	}
	if e.count(`SELECT COUNT(*) FROM events WHERE type = 'user.updated'`) != 0 {
		t.Error("a rejected patch emitted an event")
	}
}

func TestMePatchAvatarMustBeAnOwnImage(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()
	bob := e.newMember("bob", "192.168.1.51")
	setAvatar := func(m member, id string) *httptest.ResponseRecorder {
		return m.do(http.MethodPatch, "/api/v1/me", protocol.UpdateMeRequest{AvatarFileID: protocol.Patch[string]{Set: true, Value: &id}})
	}

	mine := alice.upload(t, "me.png", filestest.PlainPNG(40, 40))
	bobs := bob.upload(t, "bob.png", filestest.PlainPNG(40, 40))
	svg := alice.upload(t, "me.svg", filestest.SVGWithScript())
	doc := alice.upload(t, "cv.txt", []byte("not an image"))
	huge := alice.upload(t, "huge.png", filestest.HugePNGHeader(10000, 10000)) // stored as a plain file

	for name, id := range map[string]string{"someone else's image": bobs.ID, "an svg": svg.ID, "a text file": doc.ID, "an unprocessed image": huge.ID} {
		if err := wantStatus(t, setAvatar(alice, id), http.StatusBadRequest, CodeBadRequest); fieldErrors(err)["avatar_file_id"] == "" {
			t.Errorf("%s: details = %v", name, err.Details)
		}
	}

	rec := setAvatar(alice, mine.ID)
	wantStatus(t, rec, http.StatusOK, "")
	if me := decodeBody[protocol.Me](t, rec); me.AvatarFileID == nil || *me.AvatarFileID != mine.ID {
		t.Fatalf("avatar_file_id = %v", me.AvatarFileID)
	}
	var payload string
	if err := e.db.Read().QueryRow(`SELECT payload FROM events WHERE type = 'user.updated' ORDER BY seq DESC LIMIT 1`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"avatar_file_id":"`+mine.ID+`"`) {
		t.Errorf("payload = %s", payload)
	}

	// Messages show the avatar by URL.
	msg := alice.post(t, e.generalID(), "hello")
	if msg.Author.AvatarURL == nil || *msg.Author.AvatarURL != "/api/v1/files/"+mine.ID+"/avatar" {
		t.Errorf("avatar_url = %v", msg.Author.AvatarURL)
	}
	// Bob cannot take it, and clearing works.
	wantStatus(t, setAvatar(bob, mine.ID), http.StatusBadRequest, CodeBadRequest)
	rec = alice.do(http.MethodPatch, "/api/v1/me", `{"avatar_file_id":null}`)
	wantStatus(t, rec, http.StatusOK, "")
	if me := decodeBody[protocol.Me](t, rec); me.AvatarFileID != nil {
		t.Errorf("avatar_file_id = %v after clearing", *me.AvatarFileID)
	}
}

func TestFilesRoutesAreOnlyMountedWithAService(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()
	h := New(Deps{Version: "v", SPA: testSPA, AllowedCIDRs: e.st.AllowedCIDRs, Auth: e.svc, Chat: e.chat, Now: e.clock.Now})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/x/y", nil)
	req.RemoteAddr = "192.168.1.50:1"
	req.AddCookie(&http.Cookie{Name: cookieName, Value: alice.session()})
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no such API endpoint") {
		t.Errorf("without a files service: %d %s", rec.Code, rec.Body.String())
	}
}

// fieldErrors is details.fields of an error read from a response.
func fieldErrors(e protocol.Error) map[string]string { return e.FieldErrors() }
