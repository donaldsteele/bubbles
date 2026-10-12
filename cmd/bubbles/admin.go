package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/donaldsteele/bubbles/internal/app"
	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// adminServeFlags are the serve flags the admin commands understand; they
// resolve through config.Parse so BUBBLES_* environment variables apply.
var adminServeFlags = []struct{ name, usage string }{
	{"data-dir", "data directory"},
	{"https-addr", "HTTPS address, used for the port in printed URLs"},
}

func adminUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: bubbles admin reset-password <username> [--data-dir D]")
	fmt.Fprintln(w, "       bubbles admin create-invite [--role admin|member|guest] [--expires 72h] [--uses 1] [--data-dir D]")
	fmt.Fprintln(w, "       bubbles admin list-users [--data-dir D]")
}

// runAdmin implements `bubbles admin ...`: console access is the root of
// trust, so these commands work on the database directly (the server may be
// running; SQLite serializes the writers) and audit as actor_kind=console.
func runAdmin(args []string) int {
	return adminCmd(args, config.Options{}, app.Options{}, os.Stdout, os.Stderr)
}

// adminCmd runs an admin subcommand; appOpts injects the clock and the LAN
// addresses used to build URLs.
func adminCmd(args []string, opts config.Options, appOpts app.Options, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		adminUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "reset-password", "create-invite", "list-users":
	case "-h", "--help", "help":
		adminUsage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "bubbles admin: unknown subcommand %q\n", sub)
		adminUsage(stderr)
		return exitUsage
	}

	fs := flag.NewFlagSet("bubbles admin "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	passthrough := map[string]*string{}
	for _, f := range adminServeFlags {
		passthrough[f.name] = fs.String(f.name, "", f.usage+" (env "+config.EnvName(f.name)+")")
	}
	var role, expires string
	var uses int
	if sub == "create-invite" {
		fs.StringVar(&role, "role", string(auth.RoleMember), "role of the new account: admin, member or guest")
		fs.StringVar(&expires, "expires", auth.DefaultInviteExpiry.String(), "how long the invite is valid, for example 72h")
		fs.IntVar(&uses, "uses", 1, "how many accounts the invite can create")
	}
	positional, err := parseInterspersed(fs, rest)
	if errors.Is(err, flag.ErrHelp) {
		adminUsage(stdout)
		fs.SetOutput(stdout)
		fs.PrintDefaults()
		return exitOK
	}
	if err != nil {
		fmt.Fprintf(stderr, "bubbles admin %s: %v\n", sub, err)
		return exitUsage
	}
	wantArgs := 0
	if sub == "reset-password" {
		wantArgs = 1
	}
	if len(positional) != wantArgs {
		if wantArgs == 1 {
			fmt.Fprintf(stderr, "bubbles admin %s: expected exactly one username\n", sub)
		} else {
			fmt.Fprintf(stderr, "bubbles admin %s: unexpected argument %q\n", sub, positional[0])
		}
		adminUsage(stderr)
		return exitUsage
	}
	var cfgArgs []string
	fs.Visit(func(f *flag.Flag) {
		if p, ok := passthrough[f.Name]; ok {
			cfgArgs = append(cfgArgs, "--"+f.Name, *p)
		}
	})
	cfg, err := config.Parse(cfgArgs, opts)
	if err != nil {
		fmt.Fprintf(stderr, "bubbles admin %s: %v\n", sub, err)
		return exitUsage
	}

	now := appOpts.Now
	if now == nil {
		now = time.Now
	}
	lan := appOpts.LANAddrs
	if lan == nil {
		lan = app.DefaultLANAddrs
	}
	ctx := context.Background()
	db, err := openAdminDB(ctx, cfg, now)
	if err != nil {
		fmt.Fprintf(stderr, "bubbles admin %s: %v\n", sub, err)
		return exitRuntime
	}
	defer db.Close()
	svc, err := auth.New(auth.Options{DB: db, Settings: settings.New(db), Now: now})
	if err != nil {
		fmt.Fprintf(stderr, "bubbles admin %s: %v\n", sub, err)
		return exitRuntime
	}

	switch sub {
	case "list-users":
		err = adminListUsers(ctx, svc, stdout)
	case "reset-password":
		err = adminResetPassword(ctx, svc, positional[0], baseURL(cfg, lan), stdout, stderr)
	default:
		err = adminCreateInvite(ctx, svc, role, expires, uses, baseURL(cfg, lan), stdout, stderr)
	}
	var ve *auth.ValidationError
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &ve):
		fmt.Fprintf(stderr, "bubbles admin %s: %v\n", sub, err)
		return exitUsage
	default:
		fmt.Fprintf(stderr, "bubbles admin %s: %v\n", sub, err)
		return exitRuntime
	}
}

// parseInterspersed parses flags that may appear before or after the
// positional arguments and returns the positional ones.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// openAdminDB opens the database in an existing data directory. A fresh
// directory is prepared first; a live one is left alone (the server owns
// its permissions).
func openAdminDB(ctx context.Context, cfg config.Config, now func() time.Time) (*store.DB, error) {
	if fi, err := os.Stat(cfg.DataDir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("data directory %s does not exist (set --data-dir or %s)", cfg.DataDir, config.EnvName("data-dir"))
	}
	dbPath := filepath.Join(cfg.DataDir, store.FileName)
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		if err := config.EnsureDataDir(cfg.DataDir); err != nil {
			return nil, err
		}
	}
	db, err := store.Open(ctx, dbPath, store.Options{Now: now})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return db, nil
}

// baseURL is the HTTPS base URL clients should use: the first ranked LAN
// address (IPv6 link-local ones need a zone and are skipped) with the port
// of --https-addr, falling back to 127.0.0.1 and the default port.
func baseURL(cfg config.Config, lan func() ([]netip.Addr, error)) string {
	host := "127.0.0.1"
	if addrs, err := lan(); err == nil {
		for _, a := range addrs {
			if a.Is6() && a.IsLinkLocalUnicast() {
				continue
			}
			host = a.Unmap().WithZone("").String()
			break
		}
	}
	port := "28225"
	if _, p, err := net.SplitHostPort(cfg.HTTPSAddr); err == nil {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			port = p
		}
	}
	return "https://" + net.JoinHostPort(host, port) + "/"
}

func adminListUsers(ctx context.Context, svc *auth.Service, stdout io.Writer) error {
	users, err := svc.ListUsers(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "USERNAME\tROLE\tKIND\tSTATUS\tCREATED")
	for _, u := range users {
		status := "active"
		if !u.Active() {
			status = "deactivated"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", u.Username, u.Role, u.Kind, status, protocol.FormatTime(u.CreatedAt))
	}
	return tw.Flush()
}

func adminResetPassword(ctx context.Context, svc *auth.Service, username, base string, stdout, stderr io.Writer) error {
	user, err := svc.UserByUsername(ctx, username)
	if errors.Is(err, auth.ErrNotFound) {
		return fmt.Errorf("no user named %q", username)
	}
	if err != nil {
		return err
	}
	code, expires, err := svc.CreateReset(ctx, audit.Console, user.ID)
	if errors.Is(err, auth.ErrNotFound) {
		return fmt.Errorf("user %q cannot sign in (deactivated or an app account)", username)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%sreset/%s\n", base, code)
	fmt.Fprintf(stderr, "one-time password reset link for %s, valid until %s\n", user.Username, expires.UTC().Format(time.RFC3339))
	return nil
}

func adminCreateInvite(ctx context.Context, svc *auth.Service, roleName, expires string, uses int, base string, stdout, stderr io.Writer) error {
	role, err := auth.ParseRole(roleName)
	if err != nil {
		return &auth.ValidationError{Fields: map[string]string{"role": err.Error()}}
	}
	d, err := time.ParseDuration(expires)
	if err != nil {
		return &auth.ValidationError{Fields: map[string]string{"expires": fmt.Sprintf("%q is not a duration such as 72h", expires)}}
	}
	if d <= 0 {
		return &auth.ValidationError{Fields: map[string]string{"expires": "must be positive"}}
	}
	if uses < 1 {
		return &auth.ValidationError{Fields: map[string]string{"uses": "must be at least 1"}}
	}
	inv, code, err := svc.CreateInvite(ctx, audit.Console, auth.CreateInviteInput{Role: role, ExpiresIn: d, MaxUses: uses})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%sinvite/%s\n", base, code)
	fmt.Fprintf(stderr, "invite for role %s, %d use(s), valid until %s\n", inv.Role, inv.MaxUses,
		time.UnixMilli(inv.ExpiresAt).UTC().Format(time.RFC3339))
	return nil
}
