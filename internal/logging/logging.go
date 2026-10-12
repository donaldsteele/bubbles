// Package logging builds the server logger: JSON lines to a size-rotated file
// under the data directory plus human-readable text on the console, with
// helpers that keep secrets out of both.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
)

const (
	// FileName is the live log file inside Options.Dir.
	FileName = "bubbles.log"
	// DefaultMaxSize is the size at which the log file rotates.
	DefaultMaxSize int64 = 10 << 20
	// DefaultMaxFiles is the number of log files kept, live file included.
	DefaultMaxFiles = 5

	// Redacted replaces any value that must never reach a log.
	Redacted = "[redacted]"
)

// Options configures New. Zero values select the defaults.
type Options struct {
	Dir      string       // directory holding bubbles.log (required)
	Console  io.Writer    // text output; nil disables console logging
	Level    slog.Leveler // minimum level; nil means info
	MaxSize  int64        // rotation size in bytes; 0 means DefaultMaxSize
	MaxFiles int          // files kept, live included; 0 means DefaultMaxFiles
}

// New returns a logger that writes JSON to <Dir>/bubbles.log (mode 0600,
// rotated) and, when Console is set, text to Console. Close the returned
// closer on shutdown to flush and release the log file.
func New(opts Options) (*slog.Logger, io.Closer, error) {
	if opts.Dir == "" {
		return nil, nil, fmt.Errorf("logging: log directory is required")
	}
	maxSize, maxFiles := opts.MaxSize, opts.MaxFiles
	if maxSize == 0 {
		maxSize = DefaultMaxSize
	}
	if maxFiles == 0 {
		maxFiles = DefaultMaxFiles
	}
	file, err := NewRotatingWriter(filepath.Join(opts.Dir, FileName), maxSize, maxFiles)
	if err != nil {
		return nil, nil, err
	}
	var level slog.Leveler = slog.LevelInfo
	if opts.Level != nil {
		level = opts.Level
	}
	hopts := &slog.HandlerOptions{Level: level, ReplaceAttr: redactAttr}
	handlers := []slog.Handler{slog.NewJSONHandler(file, hopts)}
	if opts.Console != nil {
		handlers = append(handlers, slog.NewTextHandler(opts.Console, hopts))
	}
	return slog.New(slog.NewMultiHandler(handlers...)), file, nil
}

// Redact returns a stand-in for a secret. It never reveals any part of s: an
// empty s stays empty (nothing to hide) and anything else becomes Redacted.
func Redact(s string) string {
	if s == "" {
		return ""
	}
	return Redacted
}

// SecretAttr is a slog attribute for a secret value; only the redacted form
// is logged.
func SecretAttr(key, value string) slog.Attr {
	return slog.String(key, Redact(value))
}

// sensitiveParts mark an attribute key or group name as secret when they occur
// anywhere in it, matched case-insensitively. They are a safety net behind
// SecretAttr, so over-matching is preferred to leaking.
var sensitiveParts = []string{
	"password", "passphrase", "token", "secret", "authorization", "cookie",
	"apikey", "api_key", "api-key", "endpoint",
}

// sensitiveNames mark an attribute key or group name as secret only when it
// equals one of them, matched case-insensitively. They are payload or file name
// keys too generic to match by substring.
var sensitiveNames = map[string]bool{
	"body":          true,
	"content":       true,
	"text":          true,
	"filename":      true,
	"file_name":     true,
	"name_original": true,
}

// isSensitiveName reports whether an attribute key or group name is secret.
func isSensitiveName(name string) bool {
	lower := strings.ToLower(name)
	if sensitiveNames[lower] {
		return true
	}
	for _, part := range sensitiveParts {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

// redactAttr redacts an attribute whose key is secret, or that sits inside a
// group whose name is secret (for example slog.Group("token", "id", v)). Group
// values are left intact so the group keeps its shape; their children are
// visited with the group name included in groups.
func redactAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindGroup {
		return a
	}
	secret := isSensitiveName(a.Key)
	for _, g := range groups {
		if isSensitiveName(g) {
			secret = true
			break
		}
	}
	if secret {
		return slog.String(a.Key, Redact(a.Value.String()))
	}
	return a
}
