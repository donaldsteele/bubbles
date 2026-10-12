package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/donaldsteele/bubbles/internal/backup"
	"github.com/donaldsteele/bubbles/internal/config"
)

const backupUsage = "usage: bubbles backup --out FILE.bbk [--passphrase-file F] [--no-encrypt] [--data-dir D]"

// stdinIsTerminal is a variable so tests can simulate an interactive session.
var stdinIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// readPassword reads one line without echo from the terminal.
var readPassword = func() ([]byte, error) { return term.ReadPassword(int(os.Stdin.Fd())) }

func runBackup(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return backupCmd(ctx, args, config.Options{}, os.Stdout, os.Stderr)
}

func backupCmd(ctx context.Context, args []string, opts config.Options, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bubbles backup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataDir := fs.String("data-dir", "", "data directory (env "+config.EnvName("data-dir")+")")
	out := fs.String("out", "", "output file, conventionally FILE.bbk (required)")
	passFile := fs.String("passphrase-file", "", "file holding the archive passphrase")
	noEncrypt := fs.Bool("no-encrypt", false, "write an UNENCRYPTED archive (contains the CA private key)")
	if code, done := parseSub(fs, "backup", args, stderr, backupUsage); done {
		return code
	}
	if fs.NArg() > 0 || *out == "" {
		fmt.Fprintln(stderr, backupUsage)
		return exitUsage
	}
	dir, err := resolveDataDir(*dataDir, opts)
	if err != nil {
		fmt.Fprintf(stderr, "bubbles backup: %v\n", err)
		return exitUsage
	}
	bo := backup.CreateOptions{NoEncrypt: *noEncrypt}
	switch {
	case *noEncrypt:
		fmt.Fprintln(stderr, "WARNING: --no-encrypt writes the archive in the clear. It contains the CA private key")
		fmt.Fprintln(stderr, "         and every uploaded file; anyone who can read it can impersonate this server.")
	case *passFile != "":
		bo.Passphrase, err = readPassphraseFile(*passFile)
	case stdinIsTerminal():
		bo.Passphrase, err = promptPassphrase(stderr, true)
	default:
		fmt.Fprintln(stderr, "bubbles backup: no terminal to ask for a passphrase: pass --passphrase-file, or --no-encrypt to accept an unprotected archive")
		return exitUsage
	}
	if err != nil {
		fmt.Fprintf(stderr, "bubbles backup: %v\n", err)
		return exitRuntime
	}

	abs, err := filepath.Abs(*out)
	if err != nil {
		fmt.Fprintf(stderr, "bubbles backup: %v\n", err)
		return exitRuntime
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".bbk-partial-*")
	if err != nil {
		fmt.Fprintf(stderr, "bubbles backup: %v\n", err)
		return exitRuntime
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	err = backup.Create(ctx, dir, tmp, bo)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmpName, config.FileMode)
	}
	if err == nil {
		err = os.Rename(tmpName, abs)
	}
	if err != nil {
		fmt.Fprintf(stderr, "bubbles backup: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "backup written to %s\n", *out)
	return exitOK
}

// parseSub parses fs, tolerating flags after positional arguments. done is
// true when the caller should return code immediately. On success fs.Args()
// holds only the positional arguments.
func parseSub(fs *flag.FlagSet, name string, args []string, stderr io.Writer, usageLine string) (code int, done bool) {
	var pos []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				fmt.Fprintln(stderr, usageLine)
				fs.SetOutput(stderr)
				fs.PrintDefaults()
				return exitOK, true
			}
			fmt.Fprintf(stderr, "bubbles %s: %v\n", name, err)
			return exitUsage, true
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	// Parse stops at the first non-flag, so this leaves exactly pos as Args().
	_ = fs.Parse(pos)
	return 0, false
}

// resolveDataDir applies the flag, BUBBLES_DATA_DIR and the platform default.
func resolveDataDir(flagValue string, opts config.Options) (string, error) {
	var args []string
	if flagValue != "" {
		args = []string{"--data-dir", flagValue}
	}
	cfg, err := config.Parse(args, opts)
	if err != nil {
		return "", err
	}
	return cfg.DataDir, nil
}

func readPassphraseFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read passphrase file: %w", err)
	}
	p := strings.TrimRight(string(b), "\r\n")
	if p == "" {
		return "", errors.New("passphrase file is empty")
	}
	return p, nil
}

func promptPassphrase(stderr io.Writer, confirm bool) (string, error) {
	fmt.Fprint(stderr, "Passphrase: ")
	p1, err := readPassword()
	fmt.Fprintln(stderr)
	if err != nil {
		return "", fmt.Errorf("read passphrase: %w", err)
	}
	if len(p1) == 0 {
		return "", errors.New("passphrase must not be empty")
	}
	if confirm {
		fmt.Fprint(stderr, "Confirm passphrase: ")
		p2, err := readPassword()
		fmt.Fprintln(stderr)
		if err != nil {
			return "", fmt.Errorf("read passphrase: %w", err)
		}
		if string(p1) != string(p2) {
			return "", errors.New("passphrases do not match")
		}
	}
	return string(p1), nil
}
