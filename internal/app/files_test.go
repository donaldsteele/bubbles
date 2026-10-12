package app

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/files/filestest"
	"github.com/donaldsteele/bubbles/internal/retention"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
)

// TestFilesAndRetentionAreWired uploads over real TLS, attaches the file to a
// message, reads it back, and lets the registered retention job expire both.
// It also checks that the startup sweeps the upload spool.
func TestFilesAndRetentionAreWired(t *testing.T) {
	cfg := testConfig(t)
	stale := filepath.Join(cfg.DataDir, "tmp", "up-cut-off-by-a-restart")
	if err := os.WriteFile(stale, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := startCheap(t, cfg)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the stale spool file survived the startup sweep: %v", err)
	}
	// Free disk space varies by machine: do not let the reserve decide.
	if err := h.app.settings.Set(context.Background(), audit.System, settings.KeyDiskReserveBytes, 0); err != nil {
		t.Fatal(err)
	}

	base := "https://" + h.app.HTTPSAddr().String()
	client := h.httpsClient()
	do := func(req *http.Request, cookie *http.Cookie) (*http.Response, []byte) {
		req.Header.Set("Origin", base)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res, readAll(t, res)
	}
	cookie := signIn(t, h, ownerPassword).cookie

	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	part, _ := mw.CreateFormFile("file", "photo.jpg")
	_, _ = part.Write(filestest.JPEGWithMeta(64, 48, 1))
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/files", &form)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, body := do(req, cookie)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %d %s", res.StatusCode, body)
	}
	var file struct{ ID, URL string }
	if err := json.Unmarshal(body, &file); err != nil || file.ID == "" {
		t.Fatalf("upload answer %s: %v", body, err)
	}

	var channelID string
	if err := h.app.db.Read().QueryRow(`SELECT id FROM channels WHERE name = 'general'`).Scan(&channelID); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest(http.MethodPost, base+"/api/v1/channels/"+channelID+"/messages",
		strings.NewReader(`{"body":"look","file_ids":["`+file.ID+`"]}`))
	req.Header.Set("Content-Type", "application/json")
	if res, body = do(req, cookie); res.StatusCode != http.StatusCreated || !strings.Contains(string(body), file.ID) {
		t.Fatalf("post with file: %d %s", res.StatusCode, body)
	}

	req, _ = http.NewRequest(http.MethodGet, base+file.URL, nil)
	res, body = do(req, cookie)
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/jpeg" || bytes.Contains(body, []byte("Exif")) {
		t.Fatalf("download: %d %s, %d bytes", res.StatusCode, res.Header.Get("Content-Type"), len(body))
	}

	// The retention job is registered: two days later, with a one-day
	// retention on the channel, one tick removes the message and its file.
	err := h.app.db.Do(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE channels SET retention_days = 1 WHERE id = ?`, channelID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	h.clock.Add(48 * time.Hour)
	h.tickers.tick(t, retention.Interval)
	waitFor(t, "the retention job to delete the message", func() bool {
		var n int
		_ = h.app.db.Read().QueryRow(`SELECT (SELECT COUNT(*) FROM messages) + (SELECT COUNT(*) FROM files)`).Scan(&n)
		return n == 0
	})
	req, _ = http.NewRequest(http.MethodGet, base+file.URL, nil)
	if res, _ = do(req, cookie); res.StatusCode != http.StatusNotFound {
		t.Errorf("download after retention: %d", res.StatusCode)
	}
}
