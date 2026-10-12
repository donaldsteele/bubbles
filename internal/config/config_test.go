package config

import (
	"errors"
	"flag"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeEnv returns a Getenv backed by a map, plus a data directory so Parse
// does not depend on the real OS environment.
func fakeEnv(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func parse(t *testing.T, env map[string]string, args ...string) Config {
	t.Helper()
	cfg, err := Parse(args, Options{Getenv: fakeEnv(env)})
	if err != nil {
		t.Fatalf("Parse(%v): %v", args, err)
	}
	return cfg
}

func TestParseDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg := parse(t, nil, "--data-dir", dir)
	want := Config{
		DataDir:     dir,
		HTTPSAddr:   ":8443",
		HTTPAddr:    ":8080",
		MDNSName:    "bubbles",
		TLSExtraDNS: []string{},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Parse defaults = %+v, want %+v", cfg, want)
	}
}

func TestParseAllowsTwoFreePorts(t *testing.T) {
	cfg := parse(t, nil, "--data-dir", t.TempDir(), "--https-addr", "127.0.0.1:0", "--http-addr", "127.0.0.1:0")
	if cfg.HTTPSAddr != "127.0.0.1:0" || cfg.HTTPAddr != "127.0.0.1:0" {
		t.Errorf("addrs = %q %q", cfg.HTTPSAddr, cfg.HTTPAddr)
	}
	long := strings.Repeat("a", 63)
	cfg = parse(t, nil, "--data-dir", t.TempDir(), "--tls-extra-dns", "x-1.home.arpa,"+long+".lan,Nas.LAN.")
	if strings.Join(cfg.TLSExtraDNS, ",") != "x-1.home.arpa,"+long+".lan,nas.lan" {
		t.Errorf("extra DNS = %v", cfg.TLSExtraDNS)
	}
}

func TestParseAllFlags(t *testing.T) {
	dir := t.TempDir()
	cfg := parse(t, nil,
		"--data-dir", dir,
		"--https-addr", "127.0.0.1:9443",
		"--http-addr=:9080",
		"--mdns-name", "Chat",
		"--no-mdns",
		"--tls-cert", "c.pem", "--tls-key", "k.pem",
		"--tls-extra-dns", "Chat.Example.org, nas.lan. ,,",
		"--allow-public",
		"--print-config",
	)
	want := Config{
		DataDir:     dir,
		HTTPSAddr:   "127.0.0.1:9443",
		HTTPAddr:    ":9080",
		MDNSName:    "chat",
		NoMDNS:      true,
		TLSCert:     "c.pem",
		TLSKey:      "k.pem",
		TLSExtraDNS: []string{"chat.example.org", "nas.lan"},
		AllowPublic: true,
		PrintConfig: true,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Parse = %+v, want %+v", cfg, want)
	}
}

func TestParseEnvAndPrecedence(t *testing.T) {
	envDir, flagDir := t.TempDir(), t.TempDir()
	env := map[string]string{
		"BUBBLES_DATA_DIR":      envDir,
		"BUBBLES_HTTPS_ADDR":    ":7443",
		"BUBBLES_HTTP_ADDR":     ":7080",
		"BUBBLES_MDNS_NAME":     "fromenv",
		"BUBBLES_NO_MDNS":       "true",
		"BUBBLES_TLS_CERT":      "env-cert.pem",
		"BUBBLES_TLS_KEY":       "env-key.pem",
		"BUBBLES_TLS_EXTRA_DNS": "a.lan,b.lan",
		"BUBBLES_ALLOW_PUBLIC":  "1",
		"BUBBLES_PRINT_CONFIG":  "true",
	}

	fromEnv := parse(t, env)
	want := Config{
		DataDir: envDir, HTTPSAddr: ":7443", HTTPAddr: ":7080", MDNSName: "fromenv", NoMDNS: true,
		TLSCert: "env-cert.pem", TLSKey: "env-key.pem", TLSExtraDNS: []string{"a.lan", "b.lan"},
		AllowPublic: true, PrintConfig: true,
	}
	if !reflect.DeepEqual(fromEnv, want) {
		t.Errorf("env only = %+v, want %+v", fromEnv, want)
	}

	// Flags win over the environment, including turning a boolean back off.
	got := parse(t, env,
		"--data-dir", flagDir,
		"--https-addr", ":6443",
		"--mdns-name", "fromflag",
		"--no-mdns=false",
		"--tls-extra-dns", "c.lan",
		"--allow-public=false",
	)
	if got.DataDir != flagDir || got.HTTPSAddr != ":6443" || got.MDNSName != "fromflag" ||
		got.NoMDNS || got.AllowPublic || !reflect.DeepEqual(got.TLSExtraDNS, []string{"c.lan"}) {
		t.Errorf("flags did not override env: %+v", got)
	}
	// Settings not given as flags still come from the environment.
	if got.HTTPAddr != ":7080" || got.TLSCert != "env-cert.pem" || !got.PrintConfig {
		t.Errorf("env values lost: %+v", got)
	}
}

func TestParseRelativeDataDirBecomesAbsolute(t *testing.T) {
	cfg := parse(t, nil, "--data-dir", filepath.Join("some", "dir"))
	if !filepath.IsAbs(cfg.DataDir) || !strings.HasSuffix(cfg.DataDir, filepath.Join("some", "dir")) {
		t.Errorf("DataDir = %q, want absolute path ending in some/dir", cfg.DataDir)
	}
}

func TestParseAsServiceUsesServiceDefault(t *testing.T) {
	env := map[string]string{"ProgramData": `C:\PD`, "LOCALAPPDATA": `C:\LA`, "HOME": "/home/u", "USERPROFILE": `C:\U`}
	user, err := Parse(nil, Options{Getenv: fakeEnv(env)})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := Parse(nil, Options{Getenv: fakeEnv(env), AsService: true})
	if err != nil {
		t.Fatal(err)
	}
	if user.DataDir == svc.DataDir {
		t.Errorf("service and user data dirs are both %q", user.DataDir)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"unknown flag", nil, []string{"--nope"}, "nope"},
		{"positional arg", nil, []string{"extra"}, "unexpected argument"},
		{"cert without key", nil, []string{"--tls-cert", "c.pem"}, "together"},
		{"key without cert", nil, []string{"--tls-key", "k.pem"}, "together"},
		{"cert without key from env", map[string]string{"BUBBLES_TLS_CERT": "c.pem"}, nil, "together"},
		{"bad https addr", nil, []string{"--https-addr", "8443"}, "https-addr"},
		{"bad port", nil, []string{"--http-addr", ":99999"}, "http-addr"},
		{"same addrs", nil, []string{"--https-addr", ":9000", "--http-addr", ":9000"}, "differ"},
		{"bad mdns name", nil, []string{"--mdns-name", "my host.local"}, "mdns-name"},
		{"mdns name with dot", nil, []string{"--mdns-name", "a.local"}, "mdns-name"},
		{"bad extra dns", nil, []string{"--tls-extra-dns", "a b.lan"}, "tls-extra-dns"},
		{"extra dns underscore", nil, []string{"--tls-extra-dns", "my_nas.lan"}, "tls-extra-dns"},
		{"extra dns empty label", nil, []string{"--tls-extra-dns", "a..lan"}, "tls-extra-dns"},
		{"extra dns leading dot", nil, []string{"--tls-extra-dns", ".lan"}, "tls-extra-dns"},
		{"extra dns leading hyphen", nil, []string{"--tls-extra-dns", "-nas.lan"}, "tls-extra-dns"},
		{"extra dns trailing hyphen", nil, []string{"--tls-extra-dns", "nas-.lan"}, "tls-extra-dns"},
		{"extra dns wildcard", nil, []string{"--tls-extra-dns", "*.lan"}, "tls-extra-dns"},
		{"extra dns long label", nil, []string{"--tls-extra-dns", strings.Repeat("a", 64) + ".lan"}, "tls-extra-dns"},
		{"extra dns too long", nil, []string{"--tls-extra-dns", strings.Repeat("abc.", 63) + "lan"}, "tls-extra-dns"},
		{"extra dns IPv4", nil, []string{"--tls-extra-dns", "10.0.0.1"}, "tls-extra-dns"},
		{"extra dns IPv6", nil, []string{"--tls-extra-dns", "fe80::1"}, "tls-extra-dns"},
		{"extra dns from env", map[string]string{"BUBBLES_TLS_EXTRA_DNS": "ok.lan,bad_one"}, nil, "tls-extra-dns"},
		{"bad bool env", map[string]string{"BUBBLES_NO_MDNS": "maybe"}, nil, "BUBBLES_NO_MDNS"},
		{"bad bool flag", nil, []string{"--allow-public=maybe"}, "allow-public"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"--data-dir", t.TempDir()}, tt.args...)
			_, err := Parse(args, Options{Getenv: fakeEnv(tt.env)})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Parse(%v) error = %v, want one containing %q", tt.args, err, tt.want)
			}
		})
	}
}

func TestParseHelp(t *testing.T) {
	_, err := Parse([]string{"--help"}, Options{Getenv: fakeEnv(nil)})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("Parse(--help) error = %v, want flag.ErrHelp", err)
	}
	var sb strings.Builder
	WriteUsage(&sb, Options{Getenv: fakeEnv(nil)})
	for _, name := range []string{"data-dir", "https-addr", "http-addr", "mdns-name", "no-mdns", "tls-cert", "tls-key", "tls-extra-dns", "allow-public", "print-config"} {
		if !strings.Contains(sb.String(), "-"+name) || !strings.Contains(sb.String(), EnvName(name)) {
			t.Errorf("usage is missing flag %s or its env name", name)
		}
	}
}

func TestEnvName(t *testing.T) {
	if got := EnvName("tls-extra-dns"); got != "BUBBLES_TLS_EXTRA_DNS" {
		t.Errorf("EnvName = %q", got)
	}
}

func TestCheckBindPolicy(t *testing.T) {
	priv := []netip.Addr{netip.MustParseAddr("192.168.1.2")}
	tests := []struct {
		name  string
		cfg   Config
		addrs []netip.Addr
		want  error
	}{
		{"private address present", Config{}, priv, nil},
		{"no private address", Config{}, nil, ErrNoPrivateAddress},
		{"no private address, allow-public", Config{AllowPublic: true}, nil, nil},
		{"private address, allow-public", Config{AllowPublic: true}, priv, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.CheckBindPolicy(tt.addrs); !errors.Is(err, tt.want) {
				t.Errorf("CheckBindPolicy = %v, want %v", err, tt.want)
			}
		})
	}
	if msg := ErrNoPrivateAddress.Error(); !strings.Contains(msg, "--allow-public") {
		t.Errorf("error does not explain --allow-public: %q", msg)
	}
}
