package app

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/reset"
	"github.com/donaldsteele/bubbles/internal/store"
)

func TestClientIsolationHintLoggedOnce(t *testing.T) {
	logs := &syncBuffer{}
	h := startLogged(t, testConfig(t), slog.New(slog.NewTextHandler(logs, nil)), nil)
	ctx := context.Background()
	count := func() int { return strings.Count(logs.String(), "client isolation") }

	if err := h.app.checkClientIsolation(ctx); err != nil || count() != 0 {
		t.Fatalf("before setup: err=%v hints=%d", err, count())
	}
	err := h.app.db.Do(ctx, func(tx *store.Tx) error {
		_, err := audit.Write(tx, audit.System.Entry(audit.ActionSetup, "", "", nil))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	h.clock.Add(9 * time.Minute)
	if err := h.app.checkClientIsolation(ctx); err != nil || count() != 0 {
		t.Fatalf("within grace: err=%v hints=%d", err, count())
	}
	h.clock.Add(2 * time.Minute)
	for i := 0; i < 3; i++ {
		if err := h.app.checkClientIsolation(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if count() != 1 {
		t.Fatalf("hints = %d, want exactly 1\n%s", count(), logs.String())
	}
}

// A factory reset starts a new setup, so the hint must be armed again.
func TestClientIsolationHintRearmedByFactoryReset(t *testing.T) {
	logs := &syncBuffer{}
	h := startLogged(t, testConfig(t), slog.New(slog.NewTextHandler(logs, nil)), nil)
	ctx := context.Background()
	h.app.isolationDone.Store(true)
	if _, err := h.app.Reset(ctx, reset.Request{Mode: reset.ModeFactory}); err != nil {
		t.Fatal(err)
	}
	if h.app.isolationDone.Load() {
		t.Fatal("isolationDone survived a factory reset")
	}
}

func TestIncompleteResetIsReportedOnLogAndConsole(t *testing.T) {
	logs := &syncBuffer{}
	h := startLogged(t, testConfig(t), slog.New(slog.NewTextHandler(logs, nil)), nil)
	h.app.reportIncompleteReset("factory", context.DeadlineExceeded)
	for name, out := range map[string]string{"log": logs.String(), "console": h.stderr.String()} {
		if !strings.Contains(out, "restart bubbles to resume") {
			t.Errorf("%s lacks the restart hint:\n%s", name, out)
		}
	}
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Error("hint is not logged at ERROR")
	}
}
