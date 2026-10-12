package main

import (
	"bufio"
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

	"github.com/donaldsteele/bubbles/internal/app"
	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/reset"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/internal/tlsca"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

const resetUsage = "usage: bubbles reset --content | --factory [--rotate-ca] [--keep-settings] [--data-dir D] [--yes]"

func runReset(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return resetCmd(ctx, args, config.Options{}, app.Options{}, os.Stdin, os.Stdout, os.Stderr)
}

// resetCmd implements `bubbles reset`: the console reset, which works with
// the server stopped (it takes the data-dir lock) and whatever the
// web_reset_enabled setting says. appOpts injects the host name and LAN
// addresses used for a new CA.
func resetCmd(ctx context.Context, args []string, opts config.Options, appOpts app.Options, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bubbles reset", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	content := fs.Bool("content", false, "delete every message, file, alert and event; keep accounts, channels, apps and settings")
	factory := fs.Bool("factory", false, "wipe everything and return to the setup wizard")
	rotateCA := fs.Bool("rotate-ca", false, "with --factory: destroy the CA and create a new one")
	keepSettings := fs.Bool("keep-settings", false, "with --factory: keep the server settings")
	dataDir := fs.String("data-dir", "", "data directory (env "+config.EnvName("data-dir")+")")
	yes := fs.Bool("yes", false, "do not ask for the instance name")
	if code, done := parseSub(fs, "reset", args, stderr, resetUsage); done {
		return code
	}
	if fs.NArg() != 0 || *content == *factory || (*content && (*rotateCA || *keepSettings)) {
		fmt.Fprintln(stderr, resetUsage)
		return exitUsage
	}
	mode := reset.ModeContent
	if *factory {
		mode = reset.ModeFactory
	}
	dir, err := resolveDataDir(*dataDir, opts)
	if err != nil {
		fmt.Fprintf(stderr, "bubbles reset: %v\n", err)
		return exitUsage
	}
	if _, err := os.Stat(filepath.Join(dir, store.FileName)); err != nil {
		fmt.Fprintf(stderr, "bubbles reset: no bubbles database in %s\n", dir)
		return exitRuntime
	}
	lock, err := config.AcquireLock(dir)
	if errors.Is(err, config.ErrLocked) {
		fmt.Fprintf(stderr, "bubbles reset: a bubbles server is running on %s: stop it first, or reset from the admin page\n", dir)
		return exitDirInUse
	}
	if err != nil {
		fmt.Fprintf(stderr, "bubbles reset: %v\n", err)
		return exitRuntime
	}
	defer lock.Release()

	if res, err := reset.Recover(ctx, dir, reset.RecoverOptions{Now: appOpts.Now}); err != nil {
		fmt.Fprintf(stderr, "bubbles reset: complete the interrupted reset: %v\n", err)
		return exitRuntime
	} else if res != nil {
		fmt.Fprintf(stdout, "Completed an interrupted %s reset first.\n", res.Mode)
	}

	preview, name, err := resetPreview(ctx, dir, appOpts)
	if err != nil {
		fmt.Fprintf(stderr, "bubbles reset: %v\n", err)
		return exitRuntime
	}
	describeReset(stdout, dir, mode, *rotateCA, *keepSettings, preview)
	if !*yes {
		fmt.Fprintf(stdout, "\nType the instance name (%s) to confirm: ", name)
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		if strings.TrimSpace(line) != name {
			fmt.Fprintln(stderr, "bubbles reset: the confirmation does not match the instance name; nothing was deleted")
			return exitRuntime
		}
	}

	hooks := reset.Hooks{Now: appOpts.Now}
	if *rotateCA {
		domains := oldCADomains(dir)
		hooks.NewCA = func() error { return newConsoleCA(dir, domains, appOpts) }
	}
	res, err := reset.Run(ctx, dir, reset.Request{
		Mode: mode, RotateCA: *rotateCA, KeepSettings: *keepSettings, Actor: audit.Console,
	}, hooks)
	if err != nil {
		fmt.Fprintf(stderr, "bubbles reset: %v\n(run the command again or start the server to complete the reset)\n", err)
		return exitRuntime
	}
	if _, err := reset.SweepTrash(ctx, dir); err != nil {
		fmt.Fprintf(stderr, "bubbles reset: %v (the server deletes the rest when it starts)\n", err)
	}
	if mode == reset.ModeContent {
		fmt.Fprintf(stdout, "\nContent cleared (epoch %d). Devices resync when they reconnect.\n", res.Epoch)
		return exitOK
	}
	fmt.Fprintf(stdout, "\nFactory reset complete. Start the server and open it to run setup with this token:\n\n")
	fmt.Fprintf(stdout, "  %-16s %s\n", "Setup token:", res.SetupToken)
	if ca, err := tlsca.LoadCA(filepath.Join(dir, config.DirTLS)); err == nil {
		note := ""
		if res.CARotated {
			note = " (new CA: every device must trust it again)"
		}
		fmt.Fprintf(stdout, "  %-16s %s%s\n", "CA fingerprint:", ca.Fingerprint(), note)
	}
	fmt.Fprintf(stdout, "\n(the token is also in %s until setup completes)\n", filepath.Join(dir, auth.SetupTokenFile))
	return exitOK
}

// resetPreview counts what is there and reads the instance name.
func resetPreview(ctx context.Context, dir string, appOpts app.Options) (protocol.ResetPreview, string, error) {
	db, err := store.Open(ctx, filepath.Join(dir, store.FileName), store.Options{Now: appOpts.Now})
	if err != nil {
		return protocol.ResetPreview{}, "", fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	p, err := reset.Preview(ctx, db)
	if err != nil {
		return protocol.ResetPreview{}, "", err
	}
	return p, settings.New(db).InstanceName(), nil
}

func describeReset(w io.Writer, dir string, mode reset.Mode, rotateCA, keepSettings bool, p protocol.ResetPreview) {
	fmt.Fprintf(w, "This permanently deletes from %s:\n", dir)
	if mode == reset.ModeContent {
		fmt.Fprintf(w, "  - %d messages with their threads, reactions, pins, mentions and alerts\n", p.Messages)
		fmt.Fprintf(w, "  - %d files and pictures (%s)\n", p.Files, byteSize(p.Bytes))
		fmt.Fprintf(w, "  - the search index, the event history and the pre-upgrade database snapshots\n")
		fmt.Fprintf(w, "It keeps accounts, channels, memberships, apps, tokens, webhooks, invites and settings.\n")
		return
	}
	fmt.Fprintf(w, "  - %d accounts with their sessions and device tokens, and %d channels and DMs\n", p.Users, p.Channels)
	fmt.Fprintf(w, "  - %d messages and %d files and pictures (%s)\n", p.Messages, p.Files, byteSize(p.Bytes))
	fmt.Fprintf(w, "  - apps, app tokens, webhooks, invites, pairing requests, push subscriptions and the Web Push keys\n")
	fmt.Fprintf(w, "  - the audit log, the log files and every backup kept in the data directory\n")
	if !keepSettings {
		fmt.Fprintf(w, "  - the server settings (use --keep-settings to keep them)\n")
	}
	if rotateCA {
		fmt.Fprintf(w, "  - the CA: devices must trust the new one (remove the old one from them)\n")
	} else {
		fmt.Fprintf(w, "It keeps the CA, so devices stay trusted.\n")
	}
}

// oldCADomains returns the DNS names the current CA may sign for (they may
// include --tls-extra-dns values the console does not know about).
func oldCADomains(dir string) []string {
	ca, err := tlsca.LoadCA(filepath.Join(dir, config.DirTLS))
	if err != nil {
		return nil
	}
	return ca.Cert.PermittedDNSDomains
}

// newConsoleCA creates the replacement CA, permitting the same DNS names as
// the old one, and a leaf (the server re-issues the leaf at start if its
// flags add names).
func newConsoleCA(dir string, domains []string, appOpts app.Options) error {
	lan, hostname := appOpts.LANAddrs, appOpts.Hostname
	if lan == nil {
		lan = app.DefaultLANAddrs
	}
	if hostname == nil {
		hostname = os.Hostname
	}
	host, _ := hostname()
	cfg := config.Config{DataDir: dir, MDNSName: config.DefaultMDNSName}
	_, err := tlsca.NewManager(tlsca.ManagerOptions{
		Dir:  filepath.Join(dir, config.DirTLS),
		CA:   tlsca.CAOptions{InstanceName: tlsca.NormalizeHostname(host), Hostname: host, ExtraDNS: domains},
		SANs: app.SANSource(cfg, lan, hostname),
		Now:  appOpts.Now,
	})
	return err
}

func byteSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
