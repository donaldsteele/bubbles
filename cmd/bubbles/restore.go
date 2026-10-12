package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/donaldsteele/bubbles/internal/backup"
	"github.com/donaldsteele/bubbles/internal/config"
)

const restoreUsage = "usage: bubbles restore FILE.bbk [--data-dir D] [--passphrase-file F] [--force]"

func runRestore(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return restoreCmd(ctx, args, config.Options{}, os.Stdout, os.Stderr)
}

func restoreCmd(ctx context.Context, args []string, opts config.Options, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bubbles restore", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataDir := fs.String("data-dir", "", "data directory (env "+config.EnvName("data-dir")+")")
	passFile := fs.String("passphrase-file", "", "file holding the archive passphrase")
	force := fs.Bool("force", false, "replace the contents of a non-empty data directory")
	if code, done := parseSub(fs, "restore", args, stderr, restoreUsage); done {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, restoreUsage)
		return exitUsage
	}
	file := fs.Arg(0)
	dir, err := resolveDataDir(*dataDir, opts)
	if err != nil {
		fmt.Fprintf(stderr, "bubbles restore: %v\n", err)
		return exitUsage
	}
	ro := backup.RestoreOptions{Force: *force}
	if *passFile != "" {
		if ro.Passphrase, err = readPassphraseFile(*passFile); err != nil {
			fmt.Fprintf(stderr, "bubbles restore: %v\n", err)
			return exitRuntime
		}
	}
	err = restoreFile(ctx, file, dir, ro)
	if errors.Is(err, backup.ErrPassphraseRequired) && *passFile == "" {
		if !stdinIsTerminal() {
			fmt.Fprintln(stderr, "bubbles restore: the archive is encrypted: pass --passphrase-file")
			return exitUsage
		}
		if ro.Passphrase, err = promptPassphrase(stderr, false); err != nil {
			fmt.Fprintf(stderr, "bubbles restore: %v\n", err)
			return exitRuntime
		}
		err = restoreFile(ctx, file, dir, ro)
	}
	switch {
	case err == nil:
		fmt.Fprintf(stdout, "restored %s into %s\n", file, dir)
		return exitOK
	case errors.Is(err, config.ErrLocked):
		fmt.Fprintf(stderr, "bubbles restore: a bubbles server is running on %s: stop it first\n", dir)
		return exitDirInUse
	default:
		fmt.Fprintf(stderr, "bubbles restore: %v\n", err)
		return exitRuntime
	}
}

func restoreFile(ctx context.Context, file, dir string, ro backup.RestoreOptions) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	return backup.Restore(ctx, f, dir, ro)
}
