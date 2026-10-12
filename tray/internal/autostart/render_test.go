package autostart

import (
	"encoding/xml"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func exePath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}

func TestWindowsRunValue(t *testing.T) {
	exe := exePath(t, "bubbles tray.exe")
	v, err := WindowsRunValue(exe)
	if err != nil || v != `"`+exe+`" --background` {
		t.Fatalf("value = %q, %v", v, err)
	}
	for _, bad := range []string{"relative.exe", exe + `"&calc`, exe + "\nx", ""} {
		if _, err := WindowsRunValue(bad); err == nil {
			t.Errorf("WindowsRunValue(%q) accepted", bad)
		}
	}
}

func TestLaunchAgentPlist(t *testing.T) {
	exe := exePath(t, "A&B <app>")
	p, err := LaunchAgentPlist(exe)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		XMLName xml.Name `xml:"plist"`
		Dict    struct {
			Inner []byte `xml:",innerxml"`
		} `xml:"dict"`
	}
	if err := xml.Unmarshal([]byte(strings.Replace(p, `<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">`, "", 1)), &doc); err != nil {
		t.Fatalf("plist is not well-formed XML: %v\n%s", err, p)
	}
	for _, want := range []string{
		"<string>" + LaunchAgentLabel + "</string>", "<key>RunAtLoad</key>", "<true/>",
		"<string>--background</string>", "A&amp;B &lt;app&gt;",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist lacks %q:\n%s", want, p)
		}
	}
	if _, err := LaunchAgentPlist("rel/path"); err == nil {
		t.Error("relative path accepted")
	}
}

func TestDesktopEntry(t *testing.T) {
	exe := exePath(t, "my $app `x` 100%")
	e, err := DesktopEntry(exe)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(e), "\n")
	if lines[0] != "[Desktop Entry]" {
		t.Fatalf("entry = %q", e)
	}
	var execLine string
	for _, l := range lines {
		if v, ok := strings.CutPrefix(l, "Exec="); ok {
			execLine = v
		}
	}
	quoted := desktopQuote(exe)
	if execLine != quoted+" --background" || !strings.HasPrefix(execLine, `"`) {
		t.Fatalf("Exec = %q", execLine)
	}
	for _, want := range []string{`\\$app`, "\\\\`x\\\\`", "100%%"} {
		if !strings.Contains(quoted, want) {
			t.Errorf("quoted path %q lacks %q", quoted, want)
		}
	}
	for _, want := range []string{"Type=Application", "Terminal=false", "Name=bubbles"} {
		if !strings.Contains(e, want) {
			t.Errorf("entry lacks %q", want)
		}
	}
	if got := desktopQuote(`a\b"c`); got != `"a\\\\b\\"c"` {
		t.Errorf("desktopQuote = %s", got)
	}
}

// TestApplyRoundTrip exercises the real per-OS apply functions against a
// throwaway location. It is skipped unless BUBBLES_LIVE_AUTOSTART=1 because
// on Windows it writes to the real HKCU Run key.
func TestApplyRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" && !liveEnabled() {
		t.Skip("set BUBBLES_LIVE_AUTOSTART=1 to touch the HKCU Run key")
	}
	switch runtime.GOOS {
	case "linux":
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	case "darwin":
		t.Setenv("HOME", t.TempDir())
	}
	exe := exePath(t, "bubbles-tray")
	if err := Enable(exe); err != nil {
		t.Fatal(err)
	}
	on, err := Enabled()
	if err != nil || !on {
		t.Fatalf("Enabled after Enable = %v, %v", on, err)
	}
	if err := Disable(); err != nil {
		t.Fatal(err)
	}
	if on, _ := Enabled(); on {
		t.Fatal("still enabled after Disable")
	}
	if err := Disable(); err != nil {
		t.Fatalf("second Disable: %v", err)
	}
}
