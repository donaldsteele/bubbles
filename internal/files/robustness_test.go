package files

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"github.com/donaldsteele/bubbles/internal/files/filestest"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// TestFilesConcurrentUploadsCannotExceedQuota starts two uploads that each fit
// the quota on their own but not together. Both pass the up-front quota check,
// so the commit under the upload lock must refuse one of them.
func TestFilesConcurrentUploadsCannotExceedQuota(t *testing.T) {
	for round := range 5 {
		e := newTestEnv(t)
		e.set("user_quota_bytes", 1000)
		first := bytes.Repeat([]byte{byte(2*round + 1)}, 600)
		second := bytes.Repeat([]byte{byte(2*round + 2)}, 600)
		start := make(chan struct{})
		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			okCount int
			quotaNo int
			errs    []error
		)
		for i, data := range [][]byte{first, second} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := e.tryUpload("u1", []string{"a.bin", "b.bin"}[i], data)
				mu.Lock()
				defer mu.Unlock()
				var tooBig *TooLargeError
				switch {
				case err == nil:
					okCount++
				case errors.As(err, &tooBig) && tooBig.Quota:
					quotaNo++
				default:
					errs = append(errs, err)
				}
			}()
		}
		close(start)
		wg.Wait()
		if len(errs) != 0 || okCount != 1 || quotaNo != 1 {
			t.Fatalf("round %d: %d uploads stored, %d refused by quota, errors %v; want exactly one of each", round, okCount, quotaNo, errs)
		}
		if got := e.count(`SELECT COALESCE(SUM(size), 0) FROM files WHERE uploader_id = 'u1'`); got != 600 {
			t.Errorf("round %d: stored %d bytes, want 600", round, got)
		}
		if e.blobCount() != 1 || e.tmpEntries() != 0 {
			t.Errorf("round %d: %d blobs and %d spool entries left, want 1 and 0", round, e.blobCount(), e.tmpEntries())
		}
	}
}

// TestFilesOversizedGIFAndWebPAreStoredPlainWithoutDecoding uploads a GIF and
// a WebP whose headers announce more than MaxPixels. They are stored byte for
// byte as downloads, and no pixel decode runs for them.
func TestFilesOversizedGIFAndWebPAreStoredPlainWithoutDecoding(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"huge.gif", filestest.HugeGIFHeader(10000, 6000)},
		{"huge.webp", filestest.WebPWithMeta(10000, 6000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			decodes := 0
			saveHook := decodeHook
			decodeHook = func(int64) func() {
				decodes++
				return func() {}
			}
			t.Cleanup(func() { decodeHook = saveHook })

			f := e.upload("u1", tc.name, tc.data)
			if decodes != 0 {
				t.Errorf("%d decodes ran, want none", decodes)
			}
			if f.Category != protocol.FileOther || f.MetaStripped || f.HasThumb || f.Width != nil || f.Height != nil {
				t.Errorf("file = %+v, want a plain download without size or thumbnail", f)
			}
			if got := e.blob(f); !bytes.Equal(got, tc.data) {
				t.Errorf("stored %d bytes, want the %d uploaded bytes unchanged", len(got), len(tc.data))
			}
		})
	}
}
