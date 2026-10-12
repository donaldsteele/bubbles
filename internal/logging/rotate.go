package logging

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

// RotatingWriter appends to a log file and rotates it once a write would push
// it past maxSize. The live file is path; older generations are path.1
// (newest) to path.<maxFiles-1> (oldest), so at most maxFiles files exist.
// It is safe for concurrent use.
type RotatingWriter struct {
	path     string
	maxSize  int64
	maxFiles int
	openFile func(name string, flag int, perm os.FileMode) (*os.File, error)

	mu     sync.Mutex
	f      *os.File
	size   int64
	closed bool
}

// NewRotatingWriter opens (creating with mode 0600 if needed) the log file at
// path. maxSize is in bytes; maxFiles counts the live file plus its backups
// and must be at least 1 (1 means the file is truncated on rotation).
func NewRotatingWriter(path string, maxSize int64, maxFiles int) (*RotatingWriter, error) {
	if maxSize <= 0 || maxFiles < 1 {
		return nil, errors.New("logging: rotation needs a positive size and at least one file")
	}
	w := &RotatingWriter{path: path, maxSize: maxSize, maxFiles: maxFiles, openFile: os.OpenFile}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotatingWriter) open() error {
	f, err := w.openFile(w.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	w.f, w.size = f, info.Size()
	return nil
}

// Write implements io.Writer. A failed rotation is not fatal: the entry is
// appended to the current file so log lines are not lost. If the live file
// could not be reopened, Write retries the open first, so one failed rotation
// does not make the writer fail forever.
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, os.ErrClosed
	}
	if w.f == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(); w.f == nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate shifts the backups up one generation and starts a fresh live file.
// On any failure it still tries to reopen the live file so w stays usable; if
// that reopen fails too, w.f stays nil and the next Write retries it.
func (w *RotatingWriter) rotate() error {
	_ = w.f.Close()
	w.f = nil

	var first error
	note := func(err error) {
		if err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
	}
	if w.maxFiles == 1 {
		note(os.Remove(w.path))
	} else {
		note(os.Remove(w.backup(w.maxFiles - 1)))
		for i := w.maxFiles - 2; i >= 1; i-- {
			note(os.Rename(w.backup(i), w.backup(i+1)))
		}
		note(os.Rename(w.path, w.backup(1)))
	}
	if err := w.open(); err != nil {
		return err
	}
	return first
}

func (w *RotatingWriter) backup(n int) string {
	return fmt.Sprintf("%s.%d", w.path, n)
}

// Reopen closes the live file, runs clear (which may delete every log file:
// nothing holds them open meanwhile, as Windows requires) and opens a fresh
// live file. Writes wait while it runs. Factory reset uses it to wipe the
// logs of a running server.
func (w *RotatingWriter) Reopen(clear func() error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}
	var err error
	if clear != nil {
		err = clear()
	}
	if w.closed {
		return err
	}
	if oerr := w.open(); oerr != nil {
		return errors.Join(err, oerr)
	}
	return err
}

// Close closes the live file. Later writes fail with os.ErrClosed.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
