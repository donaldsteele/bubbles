// Package config resolves the bootstrap settings of `bubbles serve` from
// flags and BUBBLES_* environment variables, prepares the data directory and
// enforces single-instance and bind policy. Everything else the server needs
// lives in the settings table, not here.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/donaldsteele/bubbles/internal/dnsname"
)

// Default listen addresses and mDNS host name.
const (
	DefaultHTTPSAddr = ":8443"
	DefaultHTTPAddr  = ":8080"
	DefaultMDNSName  = "bubbles"
)

// ErrNoPrivateAddress is returned by Config.CheckBindPolicy when the host has
// no private LAN address and --allow-public was not given.
var ErrNoPrivateAddress = errors.New("no private LAN address found on this host: bubbles serves private networks only; " +
	"connect to a LAN or pass --allow-public (BUBBLES_ALLOW_PUBLIC=1) to start anyway")

// Config holds the resolved `bubbles serve` settings.
type Config struct {
	DataDir     string   `json:"data_dir"`
	HTTPSAddr   string   `json:"https_addr"`
	HTTPAddr    string   `json:"http_addr"`
	MDNSName    string   `json:"mdns_name"`
	NoMDNS      bool     `json:"no_mdns"`
	TLSCert     string   `json:"tls_cert"`
	TLSKey      string   `json:"tls_key"`
	TLSExtraDNS []string `json:"tls_extra_dns"`
	AllowPublic bool     `json:"allow_public"`
	PrintConfig bool     `json:"print_config"`
}

// Options are the inputs to Parse besides the arguments.
type Options struct {
	// Getenv looks up environment variables; nil means os.Getenv.
	Getenv func(string) string
	// AsService selects the machine-wide default data directory used when the
	// server runs as an OS service.
	AsService bool
}

func (o Options) getenv() func(string) string {
	if o.Getenv != nil {
		return o.Getenv
	}
	return os.Getenv
}

// EnvName returns the environment variable that mirrors a flag, for example
// "data-dir" becomes BUBBLES_DATA_DIR.
func EnvName(flagName string) string {
	return "BUBBLES_" + strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
}

// newFlagSet registers every serve flag, binding values into c and extraDNS.
func newFlagSet(c *Config, extraDNS *string, defDir string) *flag.FlagSet {
	fs := flag.NewFlagSet("bubbles serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	str := func(p *string, name, def, usage string) {
		fs.StringVar(p, name, def, usage+" (env "+EnvName(name)+")")
	}
	boolean := func(p *bool, name, usage string) {
		fs.BoolVar(p, name, false, usage+" (env "+EnvName(name)+")")
	}
	str(&c.DataDir, "data-dir", defDir, "data directory")
	str(&c.HTTPSAddr, "https-addr", DefaultHTTPSAddr, "HTTPS listen address")
	str(&c.HTTPAddr, "http-addr", DefaultHTTPAddr, "onboarding HTTP listen address")
	str(&c.MDNSName, "mdns-name", DefaultMDNSName, "mDNS host name, advertised as <name>.local")
	boolean(&c.NoMDNS, "no-mdns", "do not advertise over mDNS")
	str(&c.TLSCert, "tls-cert", "", "TLS certificate file (requires --tls-key; disables local issuance)")
	str(&c.TLSKey, "tls-key", "", "TLS private key file (requires --tls-cert)")
	str(extraDNS, "tls-extra-dns", "", "extra DNS names for the certificate, comma separated")
	boolean(&c.AllowPublic, "allow-public", "start even when the host has no private LAN address")
	boolean(&c.PrintConfig, "print-config", "print the resolved configuration as JSON and exit")
	return fs
}

// WriteUsage prints the serve flags, their defaults and environment names.
func WriteUsage(w io.Writer, opts Options) {
	defDir, _ := platformDataDir(opts.AsService, opts.getenv())
	var c Config
	var extra string
	fs := newFlagSet(&c, &extra, defDir)
	fs.SetOutput(w)
	fmt.Fprintln(w, "usage: bubbles serve [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "flags:")
	fs.PrintDefaults()
}

// Parse resolves the configuration for `bubbles serve`. Precedence, highest
// first: command-line flag, BUBBLES_* environment variable, built-in default.
// It prints nothing; it returns flag.ErrHelp when -h/--help was requested
// (see WriteUsage).
func Parse(args []string, opts Options) (Config, error) {
	getenv := opts.getenv()
	// A host with no discoverable default only fails if nothing overrides it.
	defDir, defErr := platformDataDir(opts.AsService, getenv)

	var c Config
	var extraDNS string
	fs := newFlagSet(&c, &extraDNS, defDir)

	// The environment seeds the flag values; flags given on the command line
	// are applied afterwards and therefore win.
	var envErr error
	fs.VisitAll(func(f *flag.Flag) {
		name := EnvName(f.Name)
		if v := getenv(name); v != "" && envErr == nil {
			if err := fs.Set(f.Name, v); err != nil {
				envErr = fmt.Errorf("%s: %w", name, err)
			}
		}
	})
	if envErr != nil {
		return Config{}, envErr
	}
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if c.DataDir == "" && defErr != nil {
		return Config{}, defErr
	}
	c.TLSExtraDNS = splitList(extraDNS)
	if err := c.normalize(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func splitList(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

var hostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// normalize validates c and rewrites values into canonical form (absolute
// data dir, lower-case names).
func (c *Config) normalize() error {
	if strings.TrimSpace(c.DataDir) == "" {
		return errors.New("data directory must not be empty")
	}
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return fmt.Errorf("resolve data directory: %w", err)
	}
	c.DataDir = abs

	for _, a := range []struct{ flag, val string }{{"https-addr", c.HTTPSAddr}, {"http-addr", c.HTTPAddr}} {
		if err := checkListenAddr(a.val); err != nil {
			return fmt.Errorf("--%s %q: %w", a.flag, a.val, err)
		}
	}
	// Port 0 asks the OS for a free port, so two ":0" addresses never clash.
	if c.HTTPSAddr == c.HTTPAddr && !strings.HasSuffix(c.HTTPSAddr, ":0") {
		return errors.New("--https-addr and --http-addr must differ")
	}

	c.MDNSName = strings.ToLower(strings.TrimSpace(c.MDNSName))
	if !hostLabel.MatchString(c.MDNSName) {
		return fmt.Errorf("--mdns-name %q must be a single DNS label (letters, digits, hyphens)", c.MDNSName)
	}

	if (c.TLSCert == "") != (c.TLSKey == "") {
		return errors.New("--tls-cert and --tls-key must be given together")
	}
	for i, d := range c.TLSExtraDNS {
		d = strings.ToLower(strings.TrimSuffix(d, "."))
		if dnsname.Valid(d) != nil {
			return fmt.Errorf("--tls-extra-dns: %q is not a DNS name (dot-separated labels of 1-63 "+
				"characters a-z, 0-9 and '-', no IP addresses)", c.TLSExtraDNS[i])
		}
		c.TLSExtraDNS[i] = d
	}
	return nil
}

func checkListenAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("want host:port or :port")
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return errors.New("port must be 0-65535")
	}
	if strings.ContainsAny(host, " \t/") {
		return errors.New("invalid host")
	}
	return nil
}

// CheckBindPolicy enforces the LAN-only policy: with no private LAN address
// (privateAddrs is the ranked list from netutil) the server refuses to start
// unless AllowPublic is set.
func (c Config) CheckBindPolicy(privateAddrs []netip.Addr) error {
	if len(privateAddrs) == 0 && !c.AllowPublic {
		return ErrNoPrivateAddress
	}
	return nil
}
