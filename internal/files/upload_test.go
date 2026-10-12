package files

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/files/filestest"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func TestFilesUploadStripsGPSAndKeepsTheImageDecodable(t *testing.T) {
	e := newTestEnv(t)
	for _, orientation := range []int{0, 1} {
		src := filestest.JPEGWithMeta(64, 48, orientation)
		f := e.upload("u1", "IMG_0042.JPG", src)
		stored := e.blob(f)
		noSecrets(t, "stored JPEG", stored, filestest.SecretXMP, filestest.SecretIPTC, filestest.SecretComment, "Exif")
		for _, code := range jpegSegments(t, stored) {
			if code == 0xE1 || code == 0xED || code == 0xFE {
				t.Errorf("orientation %d: segment %#x survived", orientation, code)
			}
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(stored))
		if err != nil || format != "jpeg" || cfg.Width != 64 || cfg.Height != 48 {
			t.Errorf("orientation %d: stored image = %+v %s %v", orientation, cfg, format, err)
		}
		if _, err := jpeg.Decode(bytes.NewReader(stored)); err != nil {
			t.Errorf("orientation %d: stored image does not decode: %v", orientation, err)
		}
		if f.Category != protocol.FileImage || f.Mime != "image/jpeg" || !f.MetaStripped || f.HasThumb {
			t.Errorf("orientation %d: file = %+v", orientation, f)
		}
		if f.Width == nil || *f.Width != 64 || *f.Height != 48 || f.Name != "IMG_0042.JPG" || f.Size != int64(len(stored)) {
			t.Errorf("orientation %d: file = %+v", orientation, f)
		}
		if f.URL != protocol.FileURL(f.ID, "IMG_0042.JPG") || f.ThumbURL != nil {
			t.Errorf("urls = %s, %v", f.URL, f.ThumbURL)
		}
	}
	if e.tmpEntries() != 0 {
		t.Errorf("%d files left in the spool", e.tmpEntries())
	}
}

func TestFilesUploadRotatesOrientation6AndKeepsTheColourProfile(t *testing.T) {
	e := newTestEnv(t)
	f := e.upload("u1", "portrait.jpg", filestest.JPEGWithMeta(64, 32, 6))
	stored := e.blob(f)
	img, err := jpeg.Decode(bytes.NewReader(stored))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 32 || b.Dy() != 64 {
		t.Fatalf("stored size = %v, want 32x64 (rotated)", b.Size())
	}
	// Rotating 90 degrees clockwise puts the red left half on top.
	top := img.At(16, 8)
	bottom := img.At(16, 56)
	if r, _, b, _ := top.RGBA(); r>>8 < 200 || b>>8 > 60 {
		t.Errorf("top pixel = %v, want red", top)
	}
	if r, _, b, _ := bottom.RGBA(); b>>8 < 200 || r>>8 > 60 {
		t.Errorf("bottom pixel = %v, want blue", bottom)
	}
	noSecrets(t, "rotated JPEG", stored, filestest.SecretXMP, filestest.SecretIPTC, filestest.SecretComment, "Exif")
	if !bytes.Contains(stored, []byte("ICC_PROFILE")) {
		t.Error("the colour profile was lost")
	}
	if o, _ := jpegMeta(bytes.NewReader(stored)); o != 1 {
		t.Errorf("orientation tag survived: %d", o)
	}
	if f.Width == nil || *f.Width != 32 || *f.Height != 64 || !f.MetaStripped {
		t.Errorf("file = %+v, want 32x64 stripped", f)
	}
}

func TestFilesUploadStripsPNGWebPAndLeavesGIF(t *testing.T) {
	e := newTestEnv(t)

	p := e.upload("u1", "shot.png", filestest.PNGWithMeta(30, 20))
	noSecrets(t, "stored PNG", e.blob(p), filestest.SecretText, "eXIf", "tEXt")
	if p.Category != protocol.FileImage || p.Mime != "image/png" || !p.MetaStripped || *p.Width != 30 || *p.Height != 20 {
		t.Errorf("png = %+v", p)
	}

	w := e.upload("u1", "pic.webp", filestest.WebPWithMeta(5, 3))
	noSecrets(t, "stored WebP", e.blob(w), filestest.SecretWebP, "Exif", "EXIF")
	if w.Category != protocol.FileImage || w.Mime != "image/webp" || !w.MetaStripped || *w.Width != 5 || *w.Height != 3 {
		t.Errorf("webp = %+v", w)
	}

	g := filestest.GIF(20, 10)
	f := e.upload("u1", "anim.gif", g)
	if !bytes.Equal(e.blob(f), g) {
		t.Error("a GIF must be stored untouched")
	}
	if f.Category != protocol.FileImage || f.Mime != "image/gif" || *f.Width != 20 {
		t.Errorf("gif = %+v", f)
	}
}

func TestFilesUploadThumbnails(t *testing.T) {
	e := newTestEnv(t)
	tests := []struct {
		name     string
		data     []byte
		w, h     int // expected thumbnail size, 0 for none
		mime     string
		wantMeta string
	}{
		{"landscape.jpg", filestest.PlainJPEG(800, 600), 640, 480, "image/jpeg", "jpeg"},
		{"portrait.jpg", filestest.PlainJPEG(600, 800), 480, 640, "image/jpeg", "jpeg"},
		{"alpha.png", filestest.AlphaPNG(700, 100), 640, 91, "image/png", "png"},
		{"opaque.png", filestest.PlainPNG(1280, 640), 640, 320, "image/jpeg", "jpeg"},
		{"first-frame.gif", filestest.GIF(700, 50), 640, 46, "image/jpeg", "jpeg"},
		{"small.jpg", filestest.PlainJPEG(640, 480), 0, 0, "", ""},
		{"tiny.png", filestest.PlainPNG(10, 10), 0, 0, "", ""},
	}
	for _, tc := range tests {
		f := e.upload("u1", tc.name, tc.data)
		if tc.w == 0 {
			if f.HasThumb || f.ThumbURL != nil {
				t.Errorf("%s: unexpected thumbnail", tc.name)
			}
			if _, err := os.Stat(e.svc.blobPath(e.row(f).SHA) + thumbSuffix); err == nil {
				t.Errorf("%s: thumbnail file exists", tc.name)
			}
			continue
		}
		if !f.HasThumb || f.ThumbURL == nil || *f.ThumbURL != protocol.FileThumbURL(f.ID, tc.name) {
			t.Errorf("%s: file = %+v", tc.name, f)
			continue
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(e.thumb(f)))
		if err != nil || format != tc.wantMeta || cfg.Width != tc.w || cfg.Height != tc.h {
			t.Errorf("%s: thumbnail = %+v %s %v, want %dx%d %s", tc.name, cfg, format, err, tc.w, tc.h, tc.wantMeta)
		}
		if got := e.row(f).ThumbMime; got != tc.mime {
			t.Errorf("%s: thumb_mime = %s, want %s", tc.name, got, tc.mime)
		}
	}
}

func TestFilesUploadStoresUnsupportedFilesAsPlainDownloads(t *testing.T) {
	e := newTestEnv(t)
	huge := filestest.HugePNGHeader(10000, 10000) // 100 MP
	corrupt := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("this is not really a jpeg")...)
	tests := []struct {
		name, mime string
		data       []byte
	}{
		{"logo.svg", "image/svg+xml", filestest.SVGWithScript()},
		{"IMG_0001.HEIC", "image/heic", append([]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"), make([]byte, 64)...)},
		{"huge.png", "image/png", huge},
		{"broken.jpg", "image/jpeg", corrupt},
		{"notes.txt", "text/plain", []byte("hello")},
		{"empty.bin", "text/plain", nil},
	}
	for _, tc := range tests {
		f := e.upload("u1", tc.name, tc.data)
		if f.Category != protocol.FileOther || f.MetaStripped || f.HasThumb || f.Width != nil || f.Height != nil {
			t.Errorf("%s: file = %+v, want a plain unprocessed file", tc.name, f)
		}
		if tc.name != "empty.bin" && f.Mime != tc.mime {
			t.Errorf("%s: mime = %s, want %s", tc.name, f.Mime, tc.mime)
		}
		if !bytes.Equal(e.blob(f), tc.data) || f.Size != int64(len(tc.data)) {
			t.Errorf("%s: stored bytes differ from the upload", tc.name)
		}
		if IsInline(f.Category, f.Mime) {
			t.Errorf("%s would be served inline", tc.name)
		}
	}
}

func TestFilesUploadIdenticalContentSharesOneBlob(t *testing.T) {
	e := newTestEnv(t)
	data := filestest.PNGWithMeta(40, 40)
	a := e.upload("u1", "a.png", data)
	b := e.upload("u2", "other-name.png", data)
	c := e.upload("u1", "again.png", filestest.PlainPNG(40, 40)) // same bytes once stripped
	if a.ID == b.ID || a.ID == c.ID {
		t.Fatal("every upload gets its own file id")
	}
	if sa, sb, sc := e.row(a).SHA, e.row(b).SHA, e.row(c).SHA; sa != sb || sa != sc {
		t.Errorf("sha256 differ: %s %s %s", sa, sb, sc)
	}
	if n := e.blobCount(); n != 1 {
		t.Errorf("%d blobs on disk, want 1", n)
	}
	if e.row(a).UploaderID != "u1" || e.row(b).UploaderID != "u2" {
		t.Error("each row keeps its uploader")
	}
	// The blob stays while any row refers to it.
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`DELETE FROM files WHERE id IN (?, ?)`, a.ID, b.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := e.svc.RemoveUnreferenced(e.ctx, []string{e.row(c).SHA}); err != nil || n != 0 {
		t.Errorf("RemoveUnreferenced removed %d (%v) while a row still refers to the blob", n, err)
	}
	if n := e.blobCount(); n != 1 {
		t.Errorf("%d blobs, want 1", n)
	}
}

func TestFilesUploadQuota(t *testing.T) {
	e := newTestEnv(t)
	data := filestest.PlainPNG(20, 20)
	n := int64(len(data))
	e.set("user_quota_bytes", n*2+n/2)

	e.upload("u1", "1.png", data)
	e.upload("u1", "2.png", data)
	_, err := e.tryUpload("u1", "3.png", data)
	var tooBig *TooLargeError
	if !errors.As(err, &tooBig) || !tooBig.Quota || tooBig.Limit != n*2+n/2 || tooBig.Used != 2*n {
		t.Fatalf("third upload: err = %v (%+v), want a quota TooLargeError", err, tooBig)
	}
	if d := tooBig.Details(); d["limit"] != "user_quota_bytes" || d["max_bytes"] != tooBig.Limit || d["used_bytes"] != tooBig.Used {
		t.Errorf("details = %v", d)
	}
	if got := e.count(`SELECT COUNT(*) FROM files WHERE uploader_id = 'u1'`); got != 2 {
		t.Errorf("%d rows, want 2", got)
	}
	// Another user has a quota of their own.
	e.upload("u2", "1.png", data)
	// Once the quota is used up the upload is refused before it is read.
	e.set("user_quota_bytes", n)
	_, err = e.svc.Upload(e.ctx, Upload{UserID: "u1", Name: "x.png", Body: errReader{}})
	if !errors.As(err, &tooBig) || !tooBig.Quota {
		t.Errorf("full quota: err = %v", err)
	}
	// 0 means unlimited.
	e.set("user_quota_bytes", 0)
	e.upload("u1", "4.png", data)
	if e.tmpEntries() != 0 {
		t.Errorf("%d files left in the spool", e.tmpEntries())
	}
}

func (e *testEnv) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.Read().QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("the body must not be read") }

func TestFilesUploadDiskReserve(t *testing.T) {
	e := newTestEnv(t)
	data := filestest.PlainPNG(10, 10)

	e.free.Store(512 << 20) // default reserve is 1 GiB
	if _, err := e.tryUpload("u1", "a.png", data); !errors.Is(err, ErrInsufficientStorage) {
		t.Fatalf("err = %v, want ErrInsufficientStorage", err)
	}
	if e.count(`SELECT COUNT(*) FROM files`) != 0 || e.blobCount() != 0 || e.tmpEntries() != 0 {
		t.Error("a refused upload left traces")
	}

	e.set("disk_reserve_bytes", 100<<20)
	e.upload("u1", "a.png", data)

	// The announced size counts too.
	_, err := e.svc.Upload(e.ctx, Upload{UserID: "u1", Name: "b.png", Body: bytes.NewReader(data), SizeHint: 500 << 20})
	if !errors.Is(err, ErrInsufficientStorage) {
		t.Errorf("with a size hint: err = %v", err)
	}
}

func TestFilesUploadMaxBytes(t *testing.T) {
	e := newTestEnv(t)
	e.set("upload_max_bytes", 1000)

	_, err := e.tryUpload("u1", "big.bin", bytes.Repeat([]byte{7}, 1001))
	var tooBig *TooLargeError
	if !errors.As(err, &tooBig) || tooBig.Quota || tooBig.Limit != 1000 || tooBig.Details()["limit"] != "upload_max_bytes" {
		t.Fatalf("err = %v (%+v)", err, tooBig)
	}
	e.upload("u1", "exact.bin", bytes.Repeat([]byte{7}, 1000))

	// A request body capped by http.MaxBytesReader is the same error.
	capped := http.MaxBytesReader(httptest.NewRecorder(), readerOf(bytes.Repeat([]byte{1}, 5000)), 100)
	if _, err := e.svc.Upload(e.ctx, Upload{UserID: "u1", Name: "x.bin", Body: capped}); !errors.As(err, &tooBig) {
		t.Errorf("MaxBytesReader: err = %v", err)
	}
	// A body that fails is a bad upload.
	if _, err := e.svc.Upload(e.ctx, Upload{UserID: "u1", Name: "x.bin", Body: errReader{}}); !errors.Is(err, ErrBadUpload) {
		t.Errorf("failing body: err = %v", err)
	}
	if e.tmpEntries() != 0 || e.count(`SELECT COUNT(*) FROM files`) != 1 {
		t.Errorf("refused uploads left traces: %d spool files, %d rows", e.tmpEntries(), e.count(`SELECT COUNT(*) FROM files`))
	}
}

type readerOf []byte

func (r readerOf) Read(p []byte) (int, error) { return copy(p, r), nil }

func (r readerOf) Close() error { return nil }

func TestFilesUploadLimitsConcurrentUploadsPerUser(t *testing.T) {
	e := newTestEnv(t)
	var releases []func()
	for range MaxConcurrentUploads {
		release, err := e.svc.Begin("u1")
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if _, err := e.svc.Begin("u1"); !errors.Is(err, ErrTooManyUploads) {
		t.Fatalf("sixth upload: err = %v, want ErrTooManyUploads", err)
	}
	other, err := e.svc.Begin("u2")
	if err != nil {
		t.Fatalf("another user is not limited: %v", err)
	}
	other()
	releases[0]()
	releases[0]() // releasing twice frees only one slot
	release, err := e.svc.Begin("u1")
	if err != nil {
		t.Fatalf("after a release: %v", err)
	}
	if _, err := e.svc.Begin("u1"); !errors.Is(err, ErrTooManyUploads) {
		t.Error("the double release freed two slots")
	}
	release()
	for _, r := range releases[1:] {
		r()
	}
	if len(e.svc.inflight) != 0 {
		t.Errorf("inflight = %v, want empty", e.svc.inflight)
	}
}

func TestFilesRemoveUnreferencedDeletesBlobAndThumbnail(t *testing.T) {
	e := newTestEnv(t)
	f := e.upload("u1", "big.jpg", filestest.PlainJPEG(800, 600))
	sha := e.row(f).SHA
	blob := e.svc.blobPath(sha)
	if !exists(blob) || !exists(blob+thumbSuffix) {
		t.Fatal("blob or thumbnail missing")
	}
	// Still referenced: nothing happens.
	if n, err := e.svc.RemoveUnreferenced(e.ctx, []string{sha}); err != nil || n != 0 || !exists(blob) {
		t.Fatalf("referenced blob: removed %d, %v", n, err)
	}
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`DELETE FROM files WHERE id = ?`, f.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	n, err := e.svc.RemoveUnreferenced(e.ctx, []string{sha, "../../etc/passwd", strings.Repeat("0", 64)})
	if err != nil || n != 1 {
		t.Fatalf("removed %d, %v; want 1", n, err)
	}
	if exists(blob) || exists(blob+thumbSuffix) {
		t.Error("blob or thumbnail still on disk")
	}
	if exists(filepath.Dir(blob)) {
		t.Error("the empty shard directory was not pruned")
	}
}

func TestFilesSweepTmpRemovesOldSpoolFiles(t *testing.T) {
	e := newTestEnv(t)
	old := filepath.Join(e.svc.tmpDir, "up-old")
	fresh := filepath.Join(e.svc.tmpDir, "up-fresh")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if n, err := e.svc.SweepTmp(24 * time.Hour); err != nil || n != 1 || exists(old) || !exists(fresh) {
		t.Fatalf("SweepTmp(24h) = %d, %v; old exists %v, fresh exists %v", n, err, exists(old), exists(fresh))
	}
	if n, err := e.svc.SweepTmp(0); err != nil || n != 1 || exists(fresh) {
		t.Fatalf("SweepTmp(0) = %d, %v", n, err)
	}
}

func TestFilesNewCreatesDirectoriesAndRequiresDependencies(t *testing.T) {
	e := newTestEnv(t)
	for _, d := range []string{e.svc.filesDir, e.svc.tmpDir} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("%s: %v", d, err)
		}
	}
	if _, err := New(Options{}); err == nil {
		t.Error("New with no options succeeded")
	}
}

func TestFilesUploadAnimatedWebPIsStrippedButGetsNoThumbnail(t *testing.T) {
	e := newTestEnv(t)
	src := filestest.AnimatedWebPWithMeta(1200, 800)
	f := e.upload("u1", "anim.webp", src)
	if f.Category != protocol.FileImage || f.Mime != "image/webp" || !f.MetaStripped {
		t.Fatalf("file = %+v", f)
	}
	if f.Width == nil || *f.Width != 1200 || *f.Height != 800 {
		t.Errorf("size = %v x %v, want the canvas 1200x800", f.Width, f.Height)
	}
	if f.HasThumb {
		t.Error("an animated WebP cannot be decoded, so it gets no thumbnail")
	}
	stored := e.blob(f)
	noSecrets(t, "stored WebP", stored, "Exif", "EXIF")
	if ids := riffChunks(t, stored); len(ids) != 3 || ids[1] != "ANIM" || ids[2] != "ANMF" {
		t.Errorf("chunks = %v, want VP8X ANIM ANMF", ids)
	}
	if stored[12+8]&0x02 == 0 {
		t.Error("the animation flag was cleared")
	}
}
