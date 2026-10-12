package files

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/files/filestest"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// forgetRow deletes the files row of f and leaves its blob behind.
func (e *testEnv) forgetRow(f protocol.File) {
	e.t.Helper()
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`DELETE FROM files WHERE id = ?`, f.ID)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

// age sets the modification time of the blob and thumbnail of sha to when.
func (e *testEnv) age(sha string, when time.Time) {
	e.t.Helper()
	for _, p := range []string{e.svc.blobPath(sha), e.svc.blobPath(sha) + thumbSuffix} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := os.Chtimes(p, when, when); err != nil {
			e.t.Fatal(err)
		}
	}
}

func TestFilesOrphanSweepRemovesOnlyOldUnreferencedBlobs(t *testing.T) {
	e := newTestEnv(t)
	past := time.Now().Add(-2 * time.Hour)
	kept := e.upload("u1", "kept.jpg", filestest.PlainJPEG(800, 600))
	e.age(e.row(kept).SHA, past)
	orphan := e.upload("u1", "gone.jpg", filestest.PlainJPEG(801, 600))
	orphanSHA := e.row(orphan).SHA
	e.forgetRow(orphan)
	e.age(orphanSHA, past)
	young := e.upload("u1", "young.bin", bytes.Repeat([]byte{7}, 300))
	youngSHA := e.row(young).SHA
	e.forgetRow(young) // unreferenced, but written just now: an upload in flight
	stray := filepath.Join(e.svc.filesDir, "README")
	if err := os.WriteFile(stray, []byte("not a blob"), 0o600); err != nil {
		t.Fatal(err)
	}

	n, err := e.svc.SweepOrphanBlobs(e.ctx, time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("SweepOrphanBlobs = %d, %v; want 1", n, err)
	}
	if exists(e.svc.blobPath(orphanSHA)) || exists(e.svc.blobPath(orphanSHA)+thumbSuffix) {
		t.Error("the old orphan or its thumbnail is still on disk")
	}
	if !exists(e.svc.blobPath(youngSHA)) {
		t.Error("a young blob (an upload in flight) was removed")
	}
	if !exists(e.svc.blobPath(e.row(kept).SHA)) || !exists(e.svc.blobPath(e.row(kept).SHA)+thumbSuffix) {
		t.Error("a referenced blob was removed")
	}
	if !exists(stray) {
		t.Error("a file that is not a blob was removed")
	}
	if n, err := e.svc.SweepOrphanBlobs(e.ctx, time.Hour); err != nil || n != 0 {
		t.Errorf("second sweep = %d, %v; want nothing left to remove", n, err)
	}
}

// TestFilesBlobRemovalRacingIdenticalUploadKeepsBlob races a removal of an
// unreferenced blob against an upload of the same bytes. Whichever wins the
// upload lock, the upload must succeed and its blob must be on disk.
func TestFilesBlobRemovalRacingIdenticalUploadKeepsBlob(t *testing.T) {
	removers := map[string]func(e *testEnv, sha string) error{
		"sweep": func(e *testEnv, sha string) error {
			_, err := e.svc.SweepOrphanBlobs(e.ctx, time.Hour)
			return err
		},
		"unreferenced": func(e *testEnv, sha string) error {
			_, err := e.svc.RemoveUnreferenced(e.ctx, []string{sha})
			return err
		},
	}
	for name, remove := range removers {
		t.Run(name, func(t *testing.T) {
			e := newTestEnv(t)
			for round := range 20 {
				data := bytes.Repeat([]byte{byte(round + 1)}, 500+round)
				old := e.upload("u1", "old.bin", data)
				sha := e.row(old).SHA
				e.forgetRow(old)
				e.age(sha, time.Now().Add(-2*time.Hour))

				var (
					wg       sync.WaitGroup
					uploaded protocol.File
					upErr    error
					rmErr    error
				)
				start := make(chan struct{})
				wg.Add(2)
				go func() {
					defer wg.Done()
					<-start
					rmErr = remove(e, sha)
				}()
				go func() {
					defer wg.Done()
					<-start
					uploaded, upErr = e.tryUpload("u2", "again.bin", data)
				}()
				close(start)
				wg.Wait()
				if upErr != nil || rmErr != nil {
					t.Fatalf("round %d: upload %v, removal %v", round, upErr, rmErr)
				}
				if got := e.row(uploaded).SHA; got != sha {
					t.Fatalf("round %d: the upload stored sha %s, want %s", round, got, sha)
				}
				if !bytes.Equal(e.blob(uploaded), data) {
					t.Fatalf("round %d: the blob of the upload is gone or wrong", round)
				}
				// Start the next round with a clean slate for this sha.
				e.forgetRow(uploaded)
			}
		})
	}
}

func TestFilesRemoveUnreferencedRespectsContext(t *testing.T) {
	e := newTestEnv(t)
	f := e.upload("u1", "x.bin", bytes.Repeat([]byte{9}, 100))
	sha := e.row(f).SHA
	e.forgetRow(f)
	ctx, cancel := context.WithCancel(e.ctx)
	cancel()
	if _, err := e.svc.RemoveUnreferenced(ctx, []string{sha}); err == nil {
		t.Error("RemoveUnreferenced with a cancelled context succeeded")
	}
}
