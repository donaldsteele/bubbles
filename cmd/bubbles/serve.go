package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"

	"github.com/donaldsteele/bubbles/internal/app"
	"github.com/donaldsteele/bubbles/internal/buildinfo"
	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/logging"
	"github.com/donaldsteele/bubbles/internal/netutil"
)

// Exit codes shared by every command (see the CLI contract).
const (
	exitOK       = 0
	exitRuntime  = 1
	exitUsage    = 2
	exitDirInUse = 3
)

// runServe implements `bubbles serve`. SIGINT and SIGTERM trigger a graceful
// shutdown (see app.DefaultShutdownTimeout).
func runServe(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, args, config.Options{}, os.Stdout, os.Stderr)
}

// serve runs the server until ctx is done.
func serve(ctx context.Context, args []string, opts config.Options, stdout, stderr io.Writer) int {
	cfg, err := config.Parse(args, opts)
	if errors.Is(err, flag.ErrHelp) {
		config.WriteUsage(stderr, opts)
		return exitOK
	}
	if err != nil {
		fmt.Fprintf(stderr, "bubbles serve: %v\nrun 'bubbles serve --help' for usage\n", err)
		return exitUsage
	}

	// --print-config only reports; it must not touch the data directory.
	if cfg.PrintConfig {
		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "bubbles serve: encode config: %v\n", err)
			return exitRuntime
		}
		fmt.Fprintf(stdout, "%s\n", out)
		return exitOK
	}

	limitMemory()

	// Take the lock before touching anything a running server owns; the
	// lock file lives in the data directory, so only its root is created
	// first. EnsureDataDir then restricts it and creates the subdirectories.
	if err := os.MkdirAll(cfg.DataDir, config.DirMode); err != nil {
		fmt.Fprintf(stderr, "bubbles serve: %v\n", err)
		return exitRuntime
	}
	lock, err := config.AcquireLock(cfg.DataDir)
	if errors.Is(err, config.ErrLocked) {
		fmt.Fprintf(stderr, "bubbles serve: another bubbles server is already using %s\n", cfg.DataDir)
		return exitDirInUse
	}
	if err != nil {
		fmt.Fprintf(stderr, "bubbles serve: %v\n", err)
		return exitRuntime
	}
	defer lock.Release()
	if err := config.EnsureDataDir(cfg.DataDir); err != nil {
		fmt.Fprintf(stderr, "bubbles serve: %v\n", err)
		return exitRuntime
	}

	cands, err := netutil.Candidates()
	if err != nil {
		fmt.Fprintf(stderr, "bubbles serve: %v\n", err)
		return exitRuntime
	}
	if err := cfg.CheckBindPolicy(netutil.Addrs(cands)); err != nil {
		fmt.Fprintf(stderr, "bubbles serve: %v\n", err)
		return exitRuntime
	}

	logger, closer, err := logging.New(logging.Options{
		Dir:     filepath.Join(cfg.DataDir, config.DirLogs),
		Console: stderr,
	})
	if err != nil {
		fmt.Fprintf(stderr, "bubbles serve: %v\n", err)
		return exitRuntime
	}
	defer closer.Close()
	slog.SetDefault(logger)
	logger.Info("starting", "version", buildinfo.Version, "data_dir", cfg.DataDir, "private_addresses", len(cands))

	appOpts := app.Options{Stderr: stderr}
	// A factory reset wipes the log directory through the open writer.
	if lf, ok := closer.(app.LogFiles); ok {
		appOpts.LogFiles = lf
	}
	a, err := app.New(ctx, cfg, logger, appOpts)
	if err != nil {
		logger.Error("cannot start", "err", err)
		return exitRuntime
	}
	if err := a.Run(ctx); err != nil {
		logger.Error("server failed", "err", err)
		return exitRuntime
	}
	return exitOK
}

// softMemoryLimit keeps the Go heap near the budget (300 MB RSS at 200
// connections); it is soft, so a burst of argon2 hashes still succeeds.
const softMemoryLimit = 200 << 20

// limitMemory applies softMemoryLimit unless the operator set GOMEMLIMIT.
func limitMemory() {
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(softMemoryLimit)
	}
}
