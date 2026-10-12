package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/donaldsteele/bubbles/internal/store"
)

// snapshotDir maps every file under dir (slash-relative path) to its content.
func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}
	return out
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// injectRenameFailure makes the nth rename call (1-based) fail; the rest
// delegate to os.Rename. The original renameFile is restored on cleanup.
func injectRenameFailure(t *testing.T, n int) {
	t.Helper()
	calls := 0
	orig := renameFile
	renameFile = func(oldpath, newpath string) error {
		calls++
		if calls == n {
			return errors.New("injected rename failure")
		}
		return os.Rename(oldpath, newpath)
	}
	t.Cleanup(func() { renameFile = orig })
}

func TestSwapInFailureLeavesLiveDirUnchanged(t *testing.T) {
	live := map[string]string{
		store.FileName:          "old-db",
		store.FileName + "-wal": "old-wal",
		VAPIDEntry:              "old-vapid",
		"setup-token.txt":       "old-token",
		dirFiles + "/a.bin":     "old-file",
		"keep.txt":              "user-data",
	}
	staged := map[string]string{
		store.FileName:      "new-db",
		VAPIDEntry:          "new-vapid",
		dirFiles + "/a.bin": "new-file",
		dirFiles + "/b.bin": "new-file-b",
	}
	// Fail each rename in turn; a failure index beyond the last rename means
	// the swap completed, which is checked separately.
	for n := 1; n <= 12; n++ {
		t.Run(fmt.Sprintf("fail-call-%d", n), func(t *testing.T) {
			dataDir := t.TempDir()
			staging := t.TempDir()
			writeFiles(t, dataDir, live)
			writeFiles(t, staging, staged)
			before := snapshotDir(t, dataDir)

			injectRenameFailure(t, n)
			if err := swapIn(staging, dataDir); err == nil {
				return
			}
			if got := snapshotDir(t, dataDir); !reflect.DeepEqual(got, before) {
				t.Fatalf("live dir changed after failed swap on call %d:\nwant %v\n got %v", n, before, got)
			}
		})
	}
}

func TestSwapInSuccessReplacesManagedAndCleansAside(t *testing.T) {
	dataDir := t.TempDir()
	staging := t.TempDir()
	writeFiles(t, dataDir, map[string]string{store.FileName: "old", "keep.txt": "user"})
	writeFiles(t, staging, map[string]string{store.FileName: "new"})

	if err := swapIn(staging, dataDir); err != nil {
		t.Fatalf("swapIn: %v", err)
	}
	want := map[string]string{store.FileName: "new", "keep.txt": "user"}
	if got := snapshotDir(t, dataDir); !reflect.DeepEqual(got, want) {
		t.Fatalf("after swap got %v want %v", got, want)
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".bubbles-restore-old-") {
			t.Fatalf("aside directory %q left behind", e.Name())
		}
	}
}
