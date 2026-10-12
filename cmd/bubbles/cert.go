package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/donaldsteele/bubbles/internal/app"
	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/tlsca"
)

// certServeFlags are the serve flags `bubbles cert` understands; they are
// resolved through config.Parse so BUBBLES_* environment variables apply.
var certServeFlags = []struct{ name, usage string }{
	{"data-dir", "data directory"},
	{"mdns-name", "mDNS host name (reissue)"},
	{"tls-extra-dns", "extra DNS names, comma separated (reissue)"},
	{"tls-cert", "bring-your-own certificate file (show)"},
	{"tls-key", "bring-your-own key file (show)"},
}

// runCert implements `bubbles cert show|reissue|export-ca`.
func runCert(args []string) int {
	return certCmd(args, config.Options{}, app.Options{}, os.Stdout, os.Stderr)
}

func certUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: bubbles cert show [--data-dir D]")
	fmt.Fprintln(w, "       bubbles cert reissue [--data-dir D]")
	fmt.Fprintln(w, "       bubbles cert export-ca [--format pem|der|mobileconfig] [--out F] [--data-dir D]")
}

// certCmd runs a cert subcommand; appOpts injects the clock, LAN addresses
// and host name used by reissue.
func certCmd(args []string, opts config.Options, appOpts app.Options, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		certUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "show", "reissue", "export-ca":
	default:
		fmt.Fprintf(stderr, "bubbles cert: unknown subcommand %q\n", sub)
		certUsage(stderr)
		return exitUsage
	}

	fs := flag.NewFlagSet("bubbles cert "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	passthrough := map[string]*string{}
	for _, f := range certServeFlags {
		passthrough[f.name] = fs.String(f.name, "", f.usage+" (env "+config.EnvName(f.name)+")")
	}
	var format, out string
	if sub == "export-ca" {
		fs.StringVar(&format, "format", "pem", "pem, der or mobileconfig")
		fs.StringVar(&out, "out", "", "output file (default stdout)")
	}
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			certUsage(stderr)
			fs.SetOutput(stderr)
			fs.PrintDefaults()
			return exitOK
		}
		fmt.Fprintf(stderr, "bubbles cert %s: %v\n", sub, err)
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "bubbles cert %s: unexpected argument %q\n", sub, fs.Arg(0))
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
		fmt.Fprintf(stderr, "bubbles cert %s: %v\n", sub, err)
		return exitUsage
	}

	switch sub {
	case "show":
		return certShow(cfg, stdout, stderr)
	case "export-ca":
		return certExport(cfg, format, out, stdout, stderr)
	default:
		return certReissue(cfg, appOpts, stdout, stderr)
	}
}

func certShow(cfg config.Config, stdout, stderr io.Writer) int {
	tlsDir := filepath.Join(cfg.DataDir, config.DirTLS)
	if cfg.TLSCert != "" {
		pair, err := tlsca.LoadBYO(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			fmt.Fprintf(stderr, "bubbles cert show: %v\n", err)
			return exitRuntime
		}
		fmt.Fprintf(stdout, "mode:             %s\n", tlsca.ModeBYO)
		fmt.Fprintf(stdout, "cert fingerprint: %s\n", tlsca.Fingerprint(pair.Leaf.Raw))
		printLeaf(stdout, pair.Leaf.NotAfter, tlsca.SANsOf(pair.Leaf))
		return exitOK
	}
	ca, err := tlsca.LoadCA(tlsDir)
	if errors.Is(err, tlsca.ErrNoCA) {
		fmt.Fprintf(stderr, "bubbles cert show: no local CA in %s yet; it is created when the server first starts\n", cfg.DataDir)
		return exitRuntime
	}
	if err != nil {
		fmt.Fprintf(stderr, "bubbles cert show: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "mode:             %s\n", tlsca.ModeLocalCA)
	fmt.Fprintf(stdout, "ca subject:       %s\n", ca.Cert.Subject.CommonName)
	fmt.Fprintf(stdout, "ca fingerprint:   %s\n", ca.Fingerprint())
	fmt.Fprintf(stdout, "ca not after:     %s\n", ca.Cert.NotAfter.UTC().Format(time.RFC3339))
	leaf, err := tlsca.LoadLeafCert(tlsDir)
	if err != nil {
		fmt.Fprintln(stdout, "leaf:             none yet (issued when the server starts)")
		return exitOK
	}
	printLeaf(stdout, leaf.NotAfter, tlsca.SANsOf(leaf))
	return exitOK
}

func printLeaf(w io.Writer, notAfter time.Time, sans tlsca.SANs) {
	fmt.Fprintf(w, "leaf not after:   %s\n", notAfter.UTC().Format(time.RFC3339))
	fmt.Fprintf(w, "leaf sans:        %s\n", strings.Join(sans.Strings(), ", "))
}

func certExport(cfg config.Config, format, out string, stdout, stderr io.Writer) int {
	var encode func(*tlsca.CA) []byte
	switch format {
	case "pem":
		encode = (*tlsca.CA).PEM
	case "der":
		encode = (*tlsca.CA).DER
	case "mobileconfig":
		encode = (*tlsca.CA).MobileConfig
	default:
		fmt.Fprintf(stderr, "bubbles cert export-ca: --format must be pem, der or mobileconfig, not %q\n", format)
		return exitUsage
	}
	ca, err := tlsca.LoadCA(filepath.Join(cfg.DataDir, config.DirTLS))
	if err != nil {
		fmt.Fprintf(stderr, "bubbles cert export-ca: %v\n", err)
		return exitRuntime
	}
	data := encode(ca)
	if out == "" || out == "-" {
		if _, err := stdout.Write(data); err != nil {
			fmt.Fprintf(stderr, "bubbles cert export-ca: %v\n", err)
			return exitRuntime
		}
		return exitOK
	}
	// The CA certificate is public; only its key is secret.
	if err := os.WriteFile(out, data, 0o644); err != nil {
		fmt.Fprintf(stderr, "bubbles cert export-ca: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stderr, "wrote %s (SHA-256 %s)\n", out, ca.Fingerprint())
	return exitOK
}

// certReissue issues a new leaf from the local CA (creating the CA on a
// fresh data directory). It needs the data directory lock, so the server
// must be stopped.
func certReissue(cfg config.Config, appOpts app.Options, stdout, stderr io.Writer) int {
	if cfg.TLSCert != "" {
		fmt.Fprintf(stderr, "bubbles cert reissue: %v\n", tlsca.ErrBYO)
		return exitUsage
	}
	if err := os.MkdirAll(cfg.DataDir, config.DirMode); err != nil {
		fmt.Fprintf(stderr, "bubbles cert reissue: %v\n", err)
		return exitRuntime
	}
	lock, err := config.AcquireLock(cfg.DataDir)
	if errors.Is(err, config.ErrLocked) {
		fmt.Fprintf(stderr, "bubbles cert reissue: a bubbles server is running on %s; stop it first "+
			"(a running server re-issues on its own when its addresses change)\n", cfg.DataDir)
		return exitDirInUse
	}
	if err != nil {
		fmt.Fprintf(stderr, "bubbles cert reissue: %v\n", err)
		return exitRuntime
	}
	defer lock.Release()
	if err := config.EnsureDataDir(cfg.DataDir); err != nil {
		fmt.Fprintf(stderr, "bubbles cert reissue: %v\n", err)
		return exitRuntime
	}
	m, err := app.CertManager(cfg, nil, appOpts)
	if err == nil {
		err = m.Reissue()
	}
	if err != nil {
		fmt.Fprintf(stderr, "bubbles cert reissue: %v\n", err)
		return exitRuntime
	}
	st := m.Status()
	fmt.Fprintf(stdout, "issued a new certificate\n")
	fmt.Fprintf(stdout, "ca fingerprint:   %s\n", st.CAFingerprint)
	fmt.Fprintf(stdout, "leaf not after:   %s\n", st.LeafNotAfter.Format(time.RFC3339))
	fmt.Fprintf(stdout, "leaf sans:        %s\n", strings.Join(st.SANs, ", "))
	return exitOK
}
