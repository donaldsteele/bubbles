package files

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/files/filestest"
)

// queued reports how many acquisitions wait on b.
func queued(b *pixelBudget) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.queue)
}

// waitQueued waits until n acquisitions wait on b.
func waitQueued(t *testing.T, b *pixelBudget, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for queued(b) != n {
		if time.Now().After(deadline) {
			t.Fatalf("%d acquisitions queued, want %d", queued(b), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPixelBudgetAdmitsWhatFits(t *testing.T) {
	b := newPixelBudget(100)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var releases []func()
	for range 3 {
		release, err := b.acquire(ctx, 30)
		if err != nil {
			t.Fatalf("acquire 30 of 100: %v", err)
		}
		releases = append(releases, release)
	}
	for _, r := range releases {
		r()
	}
	if b.used != 0 || queued(b) != 0 {
		t.Errorf("after release: used %d, queued %d", b.used, queued(b))
	}
}

func TestPixelBudgetSerializesLargeRequests(t *testing.T) {
	b := newPixelBudget(100)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var (
		mu           sync.Mutex
		active, peak int
		wg           sync.WaitGroup
	)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := b.acquire(ctx, 60)
			if err != nil {
				errs <- err
				return
			}
			defer release()
			mu.Lock()
			active++
			peak = max(peak, active)
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			active--
			mu.Unlock()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("acquire: %v", err)
	}
	if peak != 1 {
		t.Errorf("peak concurrency %d, want 1: two 60-pixel requests cannot share a 100-pixel budget", peak)
	}
}

func TestPixelBudgetOversizedRequestWaitsForExclusiveUse(t *testing.T) {
	b := newPixelBudget(100)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	small, err := b.acquire(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	granted := make(chan func(), 1)
	go func() {
		release, err := b.acquire(ctx, 1000) // clamped to the whole budget
		if err != nil {
			t.Error(err)
			return
		}
		granted <- release
	}()
	waitQueued(t, b, 1)
	select {
	case <-granted:
		t.Fatal("an oversized request was granted while 10 pixels were in use")
	default:
	}
	small()
	big := <-granted
	big()
	if b.used != 0 {
		t.Errorf("used = %d after the oversized request, want 0", b.used)
	}
}

func TestPixelBudgetCancelledWaiterGivesNothingBack(t *testing.T) {
	b := newPixelBudget(100)
	hold, err := b.acquire(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := b.acquire(ctx, 50)
		errc <- err
	}()
	waitQueued(t, b, 1)
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquire: err = %v, want context.Canceled", err)
	}
	if queued(b) != 0 {
		t.Errorf("%d waiters left after cancellation", queued(b))
	}
	hold()
	hold() // releasing twice is harmless
	if b.used != 0 {
		t.Errorf("used = %d after releases, want 0", b.used)
	}
}

// TestFilesConcurrentLargeDecodesAreSerialized uploads rotated JPEGs in
// parallel under a budget that holds one of them at a time, and checks through
// the decode hook that their decodes never overlapped.
func TestFilesConcurrentLargeDecodesAreSerialized(t *testing.T) {
	e := newTestEnv(t)
	saved := decodeBudget
	decodeBudget = newPixelBudget(2000) // two 40x40 images need 3200 pixels
	t.Cleanup(func() { decodeBudget = saved })
	var (
		mu           sync.Mutex
		active, peak int
	)
	saveHook := decodeHook
	decodeHook = func(int64) func() {
		mu.Lock()
		active++
		peak = max(peak, active)
		mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		return func() {
			mu.Lock()
			active--
			mu.Unlock()
		}
	}
	t.Cleanup(func() { decodeHook = saveHook })

	src := filestest.JPEGWithMeta(40, 40, 6)
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := range 6 {
		user := "u1"
		if i%2 == 1 {
			user = "u2"
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.tryUpload(user, "photo.jpg", src); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("upload: %v", err)
	}
	if peak != 1 {
		t.Errorf("peak concurrent decodes %d, want 1", peak)
	}
}

// TestFilesDecodeBudgetHoldsPixelsWhileDecoding checks the weight a decode asks
// for: its own pixel count, which the hook receives.
func TestFilesDecodeBudgetHoldsPixelsWhileDecoding(t *testing.T) {
	e := newTestEnv(t)
	var seen []int64
	saveHook := decodeHook
	decodeHook = func(pixels int64) func() {
		seen = append(seen, pixels)
		return func() {}
	}
	t.Cleanup(func() { decodeHook = saveHook })
	e.upload("u1", "photo.jpg", filestest.JPEGWithMeta(60, 30, 6))
	if len(seen) != 1 || seen[0] != 60*30 {
		t.Errorf("decodes weighed %v, want [1800]", seen)
	}
}
