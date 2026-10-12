package reset

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/donaldsteele/bubbles/internal/config"
)

// MarkerFile is the reset journal in the data directory. It is written
// before a reset touches anything, rewritten after every completed step and
// removed last, so a server that finds it at startup knows a reset was cut
// short and completes it before opening its listeners.
const MarkerFile = "reset-pending.json"

const markerVersion = 1

// Actor is the audit identity recorded in the marker.
type Actor struct {
	ID   string `json:"id,omitempty"`
	Kind string `json:"kind"`
	IP   string `json:"ip,omitempty"`
}

// Marker is the content of MarkerFile.
type Marker struct {
	Version      int    `json:"version"`
	ID           string `json:"id"`
	Mode         Mode   `json:"mode"`
	RotateCA     bool   `json:"rotate_ca"`
	KeepSettings bool   `json:"keep_settings"`
	// Settings is the snapshot of the settings table a factory reset with
	// KeepSettings carries over.
	Settings map[string]json.RawMessage `json:"settings,omitempty"`
	// Epoch is the instance epoch clear content moves to (current + 1,
	// decided before the first step so a resumed step cannot bump it twice).
	Epoch     int64 `json:"epoch,omitempty"`
	Actor     Actor `json:"actor"`
	StartedAt int64 `json:"started_at"`
	// Done lists the completed steps in order.
	Done []string `json:"done"`
}

func (m *Marker) done(step string) bool { return slices.Contains(m.Done, step) }

// ReadMarker returns the pending reset of dataDir, or nil when there is none.
func ReadMarker(dataDir string) (*Marker, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, MarkerFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", MarkerFile, err)
	}
	var m Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("read %s: %w", MarkerFile, err)
	}
	if m.Version != markerVersion {
		return nil, fmt.Errorf("read %s: unsupported version %d", MarkerFile, m.Version)
	}
	if m.Mode != ModeContent && m.Mode != ModeFactory {
		return nil, fmt.Errorf("read %s: unknown mode %q", MarkerFile, m.Mode)
	}
	return &m, nil
}

// WriteMarker replaces the marker of dataDir atomically (temporary file,
// fsync, rename) with owner-only permissions.
func WriteMarker(dataDir string, m *Marker) error {
	if m.Version == 0 {
		m.Version = markerVersion
	}
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode %s: %w", MarkerFile, err)
	}
	path := filepath.Join(dataDir, MarkerFile)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, config.FileMode)
	if err != nil {
		return fmt.Errorf("write %s: %w", MarkerFile, err)
	}
	_, werr := f.Write(b)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", MarkerFile, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", MarkerFile, err)
	}
	return nil
}

// RemoveMarker deletes the marker; a missing marker is not an error.
func RemoveMarker(dataDir string) error {
	err := os.Remove(filepath.Join(dataDir, MarkerFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", MarkerFile, err)
	}
	return nil
}
