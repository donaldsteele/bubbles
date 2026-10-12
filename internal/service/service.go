// Package service installs bubbles as an operating-system service: a Windows
// service (SCM), a macOS LaunchDaemon or a Linux systemd unit.
//
// Everything that executes a command goes through an injectable Runner and
// every file write through Options, so unit tests assert the exact rendered
// unit, plist and command lines without touching the host. The render and
// command-list functions are pure and compiled on every platform.
package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/donaldsteele/bubbles/internal/config"
)

// Names of the installed service on each platform.
const (
	WindowsServiceName = "bubbles"
	LaunchdLabel       = "com.bubbles.server"
	LaunchdPlistPath   = "/Library/LaunchDaemons/com.bubbles.server.plist"
	SystemdUnitName    = "bubbles.service"
	SystemdUnitPath    = "/etc/systemd/system/bubbles.service"
	LinuxUser          = "bubbles"
)

// ErrNotAdmin means the process lacks the privileges to manage the service.
var ErrNotAdmin = errors.New("installing a service requires administrator privileges: run as root (sudo) or from an elevated prompt")

// ErrUnsupported is returned on platforms without a service manager integration.
var ErrUnsupported = errors.New("service management is not supported on this platform")

// Spec describes the service to install.
type Spec struct {
	// Exe is the absolute path of the bubbles binary.
	Exe string
	// DataDir is the service data directory.
	DataDir string
	// HTTPSAddr and HTTPAddr are passed to serve only when they differ from
	// the defaults; they also decide the firewall ports.
	HTTPSAddr string
	HTTPAddr  string
}

// ServeArgs returns the argument list that starts the server for this spec.
func (s Spec) ServeArgs() []string {
	args := []string{"serve", "--data-dir", s.DataDir}
	if s.HTTPSAddr != "" && s.HTTPSAddr != config.DefaultHTTPSAddr {
		args = append(args, "--https-addr", s.HTTPSAddr)
	}
	if s.HTTPAddr != "" && s.HTTPAddr != config.DefaultHTTPAddr {
		args = append(args, "--http-addr", s.HTTPAddr)
	}
	return args
}

// Ports returns the HTTPS and onboarding (HTTP) TCP ports.
func (s Spec) Ports() (https, http int, err error) {
	https, err = portOf(s.HTTPSAddr, config.DefaultHTTPSAddr)
	if err != nil {
		return 0, 0, err
	}
	http, err = portOf(s.HTTPAddr, config.DefaultHTTPAddr)
	return https, http, err
}

func portOf(addr, def string) (int, error) {
	if addr == "" {
		addr = def
	}
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("invalid port in listen address %q", addr)
	}
	return n, nil
}

// Runner executes an external command and returns its combined output.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// ExecRunner runs real commands.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Options injects the side effects of a Manager.
type Options struct {
	Runner    Runner
	IsAdmin   func() bool
	WriteFile func(path string, data []byte, mode os.FileMode) error
	Remove    func(path string) error
}

func (o Options) withDefaults() Options {
	if o.Runner == nil {
		o.Runner = ExecRunner{}
	}
	if o.IsAdmin == nil {
		o.IsAdmin = isAdmin
	}
	if o.WriteFile == nil {
		o.WriteFile = os.WriteFile
	}
	if o.Remove == nil {
		o.Remove = os.Remove
	}
	return o
}

// Manager controls the installed service.
type Manager interface {
	Install(ctx context.Context, spec Spec) error
	Uninstall(ctx context.Context) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	// Status returns a short human-readable state such as "active" or "running".
	Status(ctx context.Context) (string, error)
}

// New returns the Manager for the running platform.
func New(opts Options) Manager { return platformManager(opts.withDefaults()) }

// DefaultSpec returns the spec for the running binary with the platform's
// service data directory.
func DefaultSpec() (Spec, error) {
	exe, err := os.Executable()
	if err != nil {
		return Spec{}, fmt.Errorf("locate executable: %w", err)
	}
	cfg, err := config.Parse(nil, config.Options{AsService: true, Getenv: func(string) string { return "" }})
	if err != nil {
		return Spec{}, err
	}
	return Spec{Exe: exe, DataDir: cfg.DataDir}, nil
}

func runAll(ctx context.Context, r Runner, cmds [][]string) error {
	for _, c := range cmds {
		if out, err := r.Run(ctx, c[0], c[1:]...); err != nil {
			return commandError(c, out, err)
		}
	}
	return nil
}

func commandError(c []string, out string, err error) error {
	if out != "" {
		return fmt.Errorf("%s: %w: %s", strings.Join(c, " "), err, out)
	}
	return fmt.Errorf("%s: %w", strings.Join(c, " "), err)
}
