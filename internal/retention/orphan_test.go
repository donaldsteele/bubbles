package retention

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// orphanBlob uploads data, deletes its row and dates its blob to age before
// the fake clock. It returns the SHA-256 of the blob.
func (e *env) orphanBlob(data []byte, age time.Duration) string {
	e.t.Helper()
	f := e.upload("gone.bin", data)
	sha := e.sha(f)
	e.exec(`DELETE FROM files WHERE id = ?`, f.ID)
	when := e.clock.Now().Add(-age)
	blob := filepath.Join(e.dir, "files", sha[:2], sha[2:4], sha)
	if err := os.Chtimes(blob, when, when); err != nil {
		e.t.Fatal(err)
	}
	return sha
}

func TestRetentionOrphanBlobSweepRunsOncePerDay(t *testing.T) {
	e := newEnv(t)
	stale := e.orphanBlob(bytes.Repeat([]byte{1}, 100), 2*time.Hour)
	young := e.orphanBlob(bytes.Repeat([]byte{2}, 100), 10*time.Minute)
	res, err := e.job.RunOnce(e.ctx)
	if err != nil || res.OrphanBlobs != 1 {
		t.Fatalf("first run = %+v, %v; want one orphan blob", res, err)
	}
	if e.blobShaExists(stale) || !e.blobShaExists(young) {
		t.Error("the sweep removed the wrong blob, or kept the stale one")
	}

	// Within the day the sweep is not due again.
	e.clock.Advance(time.Hour)
	later := e.orphanBlob(bytes.Repeat([]byte{3}, 100), 2*time.Hour)
	if res, err := e.job.RunOnce(e.ctx); err != nil || res.OrphanBlobs != 0 || !e.blobShaExists(later) {
		t.Fatalf("run within the day = %+v, %v; the sweep must not be due", res, err)
	}

	// A day after the first run it is due, and both leftovers are old enough.
	e.clock.Advance(OrphanSweepInterval)
	if res, err := e.job.RunOnce(e.ctx); err != nil || res.OrphanBlobs != 2 {
		t.Fatalf("run a day later = %+v, %v; want two orphan blobs", res, err)
	}
	if e.blobShaExists(young) || e.blobShaExists(later) {
		t.Error("orphan blobs survived the daily sweep")
	}
}
