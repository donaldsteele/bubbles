package files

import (
	"context"
	"slices"
	"sync"
)

// MaxDecodePixels is the budget of pixels that the whole process decodes,
// rotates or thumbnails at once. A rotated 50 MP JPEG peaks near 0.5 to 0.8 GB,
// and a Raspberry Pi 4 cannot hold five of them, so decodes queue up instead.
const MaxDecodePixels = 64_000_000

// decodeBudget is the process-wide decode budget. An image holds its pixel
// count from before its decode until its thumbnail is made.
var decodeBudget = newPixelBudget(MaxDecodePixels)

// decodeHook is told when a decode starts, with its pixel count, and returns
// the function called when it ends. Tests use it to count concurrent decodes.
var decodeHook = func(int64) func() { return func() {} }

// pixelBudget is a weighted semaphore over pixels. Waiters are served in
// arrival order, so a large image is not starved by a stream of small ones. A
// request above the capacity is clamped to it and so waits for exclusive use.
type pixelBudget struct {
	capacity int64

	mu    sync.Mutex
	used  int64
	queue []*budgetWaiter
}

type budgetWaiter struct {
	n     int64
	ready chan struct{} // closed once the pixels were granted
}

func newPixelBudget(capacity int64) *pixelBudget {
	return &pixelBudget{capacity: max(capacity, 1)}
}

// acquire waits until n pixels (clamped to the capacity) fit in the budget and
// returns the function that gives them back; calling it twice is harmless. It
// returns the context's error when the context ends first.
func (b *pixelBudget) acquire(ctx context.Context, n int64) (release func(), err error) {
	n = max(1, min(n, b.capacity))
	b.mu.Lock()
	if len(b.queue) == 0 && b.used+n <= b.capacity {
		b.used += n
		b.mu.Unlock()
		return b.releaser(n), nil
	}
	w := &budgetWaiter{n: n, ready: make(chan struct{})}
	b.queue = append(b.queue, w)
	b.mu.Unlock()

	select {
	case <-w.ready:
		return b.releaser(n), nil
	case <-ctx.Done():
		b.mu.Lock()
		defer b.mu.Unlock()
		select {
		case <-w.ready:
			// Granted as the context ended: give the pixels back.
			b.used -= n
		default:
			b.queue = slices.DeleteFunc(b.queue, func(x *budgetWaiter) bool { return x == w })
		}
		b.grant()
		return nil, ctx.Err()
	}
}

// releaser returns the function that gives n pixels back, once.
func (b *pixelBudget) releaser(n int64) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.used -= n
			b.grant()
		})
	}
}

// grant hands pixels to the waiters at the head of the queue while they fit.
// The caller holds b.mu.
func (b *pixelBudget) grant() {
	for len(b.queue) > 0 && b.used+b.queue[0].n <= b.capacity {
		w := b.queue[0]
		b.queue[0] = nil
		b.queue = b.queue[1:]
		b.used += w.n
		close(w.ready)
	}
}
