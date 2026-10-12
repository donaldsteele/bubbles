package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls [][]string
	fail  map[string]error
	out   map[string]string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	key := strings.Join(call, " ")
	return f.out[key], f.fail[key]
}

type fakeFS struct {
	files   map[string]string
	removed []string
}

func (f *fakeFS) write(p string, d []byte, _ os.FileMode) error {
	if f.files == nil {
		f.files = map[string]string{}
	}
	f.files[p] = string(d)
	return nil
}

func (f *fakeFS) remove(p string) error {
	f.removed = append(f.removed, p)
	return nil
}

func newTestManager(t *testing.T, mk func(Options) Manager, admin bool, r *fakeRunner, fsys *fakeFS) Manager {
	t.Helper()
	return mk(Options{Runner: r, IsAdmin: func() bool { return admin }, WriteFile: fsys.write, Remove: fsys.remove}.withDefaults())
}

func normalize(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

func TestDeployFilesMatchTemplates(t *testing.T) {
	unit, err := RenderSystemdUnit(Spec{Exe: "/usr/local/bin/bubbles", DataDir: "/var/lib/bubbles"})
	if err != nil {
		t.Fatal(err)
	}
	plist := RenderLaunchdPlist(Spec{Exe: "/usr/local/bin/bubbles", DataDir: "/Library/Application Support/bubbles"})
	for file, want := range map[string]string{
		"../../deploy/systemd/bubbles.service":          unit,
		"../../deploy/launchd/com.bubbles.server.plist": plist,
	} {
		got, err := os.ReadFile(filepath.FromSlash(file))
		if err != nil {
			t.Fatal(err)
		}
		if normalize(string(got)) != want {
			t.Errorf("%s is out of sync with the rendered template", file)
		}
	}
	readme, err := os.ReadFile(filepath.FromSlash("../../deploy/windows/README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range FirewallAddCommands(8443, 8080) {
		manual := strings.Join(c[:3], " ") + " " + strings.Join(c[3:5], " ") + ` name="` + strings.TrimPrefix(c[5], "name=") + `" ` + strings.Join(c[6:], " ")
		if !strings.Contains(normalize(string(readme)), manual) {
			t.Errorf("windows README lacks %q", manual)
		}
	}
}

func TestRenderSystemdUnitHardening(t *testing.T) {
	unit, err := RenderSystemdUnit(Spec{Exe: "/opt/b/bubbles", DataDir: "/srv/b", HTTPSAddr: ":9443", HTTPAddr: ":8080"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ExecStart=/opt/b/bubbles serve --data-dir /srv/b --https-addr :9443\n",
		"ReadWritePaths=/srv/b\n", "NoNewPrivileges=yes\n", "ProtectSystem=strict\n",
		"ProtectHome=yes\n", "PrivateTmp=yes\n", "Restart=on-failure\n", "User=bubbles\n",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q", want)
		}
	}
	if _, err := RenderSystemdUnit(Spec{Exe: "/x", DataDir: "/with space"}); err == nil {
		t.Error("data dir with a space must be rejected")
	}
}

func TestRenderSystemdRejectsSpecifierAndQuotes(t *testing.T) {
	for _, dir := range []string{"/srv/%h/b", "/srv/100%", "/srv/b\"c", "/srv/b'c", `/srv/b\c`, "/srv/b c"} {
		if _, err := RenderSystemdUnit(Spec{Exe: "/x", DataDir: dir}); err == nil {
			t.Errorf("data dir %q must be rejected", dir)
		}
	}
	if _, err := RenderSystemdUnit(Spec{Exe: "/x", DataDir: "/srv/bubbles"}); err != nil {
		t.Errorf("plain data dir rejected: %v", err)
	}
}

func TestRenderLaunchdPlistEscapes(t *testing.T) {
	p := RenderLaunchdPlist(Spec{Exe: "/a&b/bubbles", DataDir: "/d<1>"})
	for _, want := range []string{"<string>/a&amp;b/bubbles</string>", "<string>/d&lt;1&gt;</string>", "<key>RunAtLoad</key>\n\t<true/>", "<key>KeepAlive</key>\n\t<true/>"} {
		if !strings.Contains(p, want) {
			t.Errorf("plist lacks %q", want)
		}
	}
}

func TestFirewallCommandsExact(t *testing.T) {
	want := [][]string{
		{"netsh", "advfirewall", "firewall", "add", "rule", "name=bubbles HTTPS", "dir=in", "action=allow", "protocol=TCP", "localport=8443", "profile=private"},
		{"netsh", "advfirewall", "firewall", "add", "rule", "name=bubbles onboarding", "dir=in", "action=allow", "protocol=TCP", "localport=8080", "profile=private"},
	}
	if got := FirewallAddCommands(8443, 8080); !reflect.DeepEqual(got, want) {
		t.Errorf("add = %v", got)
	}
	wantDel := [][]string{
		{"netsh", "advfirewall", "firewall", "delete", "rule", "name=bubbles HTTPS"},
		{"netsh", "advfirewall", "firewall", "delete", "rule", "name=bubbles onboarding"},
	}
	if got := FirewallDeleteCommands(); !reflect.DeepEqual(got, wantDel) {
		t.Errorf("delete = %v", got)
	}
}

func TestSpecPorts(t *testing.T) {
	h, o, err := Spec{}.Ports()
	if err != nil || h != 8443 || o != 8080 {
		t.Fatalf("defaults = %d %d %v", h, o, err)
	}
	h, o, err = Spec{HTTPSAddr: "0.0.0.0:9443", HTTPAddr: ":9080"}.Ports()
	if err != nil || h != 9443 || o != 9080 {
		t.Fatalf("custom = %d %d %v", h, o, err)
	}
	if _, _, err := (Spec{HTTPSAddr: "nonsense"}).Ports(); err == nil {
		t.Error("invalid address accepted")
	}
}

func TestSystemdInstallCommands(t *testing.T) {
	spec := Spec{Exe: "/usr/local/bin/bubbles", DataDir: "/var/lib/bubbles"}
	for _, userExists := range []bool{false, true} {
		r := &fakeRunner{fail: map[string]error{}}
		if userExists {
			r.out = map[string]string{}
		} else {
			r.fail["id -u bubbles"] = errors.New("no such user")
		}
		fsys := &fakeFS{}
		m := newTestManager(t, newSystemd, true, r, fsys)
		if err := m.Install(context.Background(), spec); err != nil {
			t.Fatal(err)
		}
		var want [][]string
		want = append(want, []string{"id", "-u", "bubbles"})
		if !userExists {
			want = append(want, []string{"useradd", "--system", "--user-group", "--home-dir", "/var/lib/bubbles", "--no-create-home", "--shell", "/usr/sbin/nologin", "bubbles"})
		}
		want = append(want,
			[]string{"install", "-d", "-m", "0700", "-o", "bubbles", "-g", "bubbles", "/var/lib/bubbles"},
			[]string{"systemctl", "daemon-reload"},
			[]string{"systemctl", "enable", "bubbles.service"},
			[]string{"systemctl", "start", "bubbles.service"})
		if !reflect.DeepEqual(r.calls, want) {
			t.Errorf("userExists=%v calls:\n got %v\nwant %v", userExists, r.calls, want)
		}
		unit, _ := RenderSystemdUnit(spec)
		if fsys.files[SystemdUnitPath] != unit {
			t.Error("unit file not written verbatim")
		}
	}
}

func TestSystemdUninstallStartStopStatus(t *testing.T) {
	r := &fakeRunner{out: map[string]string{"systemctl is-active bubbles.service": "inactive"}, fail: map[string]error{"systemctl is-active bubbles.service": errors.New("exit 3")}}
	fsys := &fakeFS{}
	m := newTestManager(t, newSystemd, true, r, fsys)
	ctx := context.Background()
	if err := m.Uninstall(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := m.Status(ctx)
	if err != nil || st != "inactive" {
		t.Fatalf("Status = %q, %v", st, err)
	}
	want := [][]string{
		{"systemctl", "disable", "--now", "bubbles.service"},
		{"systemctl", "daemon-reload"},
		{"systemctl", "start", "bubbles.service"},
		{"systemctl", "stop", "bubbles.service"},
		{"systemctl", "is-active", "bubbles.service"},
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls:\n got %v\nwant %v", r.calls, want)
	}
	if !reflect.DeepEqual(fsys.removed, []string{SystemdUnitPath}) {
		t.Errorf("removed = %v", fsys.removed)
	}
}

func TestLaunchdLifecycle(t *testing.T) {
	spec := Spec{Exe: "/usr/local/bin/bubbles", DataDir: "/Library/Application Support/bubbles"}
	r := &fakeRunner{}
	fsys := &fakeFS{}
	m := newTestManager(t, newLaunchd, true, r, fsys)
	ctx := context.Background()
	if err := m.Install(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if fsys.files[LaunchdPlistPath] != RenderLaunchdPlist(spec) {
		t.Error("plist not written verbatim")
	}
	want := [][]string{
		{"install", "-d", "-m", "0700", "/Library/Application Support/bubbles"},
		{"launchctl", "bootout", "system/com.bubbles.server"},
		{"launchctl", "bootstrap", "system", "/Library/LaunchDaemons/com.bubbles.server.plist"},
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("install calls:\n got %v\nwant %v", r.calls, want)
	}
	r.calls = nil
	if err := m.Uninstall(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.calls, [][]string{{"launchctl", "bootout", "system/com.bubbles.server"}}) || !reflect.DeepEqual(fsys.removed, []string{LaunchdPlistPath}) {
		t.Errorf("uninstall calls %v removed %v", r.calls, fsys.removed)
	}
	r.calls = nil
	r.fail = map[string]error{"launchctl bootstrap system /Library/LaunchDaemons/com.bubbles.server.plist": errors.New("already loaded")}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.calls[len(r.calls)-1]; !reflect.DeepEqual(got, []string{"launchctl", "kickstart", "-k", "system/com.bubbles.server"}) {
		t.Errorf("start fallback = %v", got)
	}
	r.out = map[string]string{"launchctl print system/com.bubbles.server": "x = y\n\tstate = running\n"}
	if st, _ := m.Status(ctx); st != "running" {
		t.Errorf("Status = %q", st)
	}
}

func TestNonAdminIsRefused(t *testing.T) {
	for name, mk := range map[string]func(Options) Manager{"systemd": newSystemd, "launchd": newLaunchd} {
		r := &fakeRunner{}
		fsys := &fakeFS{}
		m := newTestManager(t, mk, false, r, fsys)
		ctx := context.Background()
		for op, err := range map[string]error{
			"install":   m.Install(ctx, Spec{Exe: "/x", DataDir: "/d"}),
			"uninstall": m.Uninstall(ctx),
			"start":     m.Start(ctx),
			"stop":      m.Stop(ctx),
		} {
			if !errors.Is(err, ErrNotAdmin) {
				t.Errorf("%s %s = %v, want ErrNotAdmin", name, op, err)
			}
		}
		if len(r.calls) != 0 || len(fsys.files) != 0 {
			t.Errorf("%s: side effects without privileges: %v %v", name, r.calls, fsys.files)
		}
	}
}

func TestServeArgsOmitDefaults(t *testing.T) {
	got := Spec{DataDir: "/d", HTTPSAddr: ":8443", HTTPAddr: ":8080"}.ServeArgs()
	if !reflect.DeepEqual(got, []string{"serve", "--data-dir", "/d"}) {
		t.Errorf("ServeArgs = %v", got)
	}
}

func TestLiveServiceGated(t *testing.T) {
	if os.Getenv("BUBBLES_LIVE_SERVICE") != "1" {
		t.Skip("set BUBBLES_LIVE_SERVICE=1 to install a real service (requires administrator privileges)")
	}
	spec, err := DefaultSpec()
	if err != nil {
		t.Fatal(err)
	}
	m := New(Options{})
	ctx := context.Background()
	if err := m.Install(ctx, spec); err != nil {
		t.Fatalf("Install: %v", err)
	}
	defer m.Uninstall(ctx)
	if st, err := m.Status(ctx); err != nil || st == "" {
		t.Fatalf("Status = %q, %v", st, err)
	}
}
