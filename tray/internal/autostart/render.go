// Package autostart registers the tray app to start at login: the HKCU Run
// key on Windows, a LaunchAgent on macOS and an XDG autostart entry on
// Linux. The rendering of the files and values is plain Go and testable on
// every OS; only the small apply functions are per-OS.
package autostart

import (
	"errors"
	"path/filepath"
	"strings"
)

const (
	// RunValueName is the name of the Windows Run value.
	RunValueName = "bubbles-tray"
	// LaunchAgentLabel is the label (and file name stem) of the macOS agent.
	LaunchAgentLabel = "com.bubbles.tray"
	// DesktopFileName is the file name of the XDG autostart entry.
	DesktopFileName = "bubbles-tray.desktop"
	// BackgroundArg makes a login start run in the background without
	// opening a window.
	BackgroundArg = "--background"
)

// ErrUnsupported is returned on operating systems without a login mechanism
// this package knows.
var ErrUnsupported = errors.New("start at login is not supported on this system")

func checkExe(exe string) error {
	if !filepath.IsAbs(exe) {
		return errors.New("the executable path must be absolute")
	}
	if strings.ContainsAny(exe, "\x00\r\n\"") {
		return errors.New("the executable path contains unsupported characters")
	}
	return nil
}

// WindowsRunValue is the value of the HKCU\Software\Microsoft\Windows\
// CurrentVersion\Run entry: the quoted path plus the background flag.
func WindowsRunValue(exe string) (string, error) {
	if err := checkExe(exe); err != nil {
		return "", err
	}
	return `"` + exe + `" ` + BackgroundArg, nil
}

// LaunchAgentPlist renders the macOS LaunchAgent property list.
func LaunchAgentPlist(exe string) (string, error) {
	if err := checkExe(exe); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	b.WriteString("\t<key>Label</key>\n\t<string>" + LaunchAgentLabel + "</string>\n")
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	b.WriteString("\t\t<string>" + xmlEscape(exe) + "</string>\n")
	b.WriteString("\t\t<string>" + BackgroundArg + "</string>\n")
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>ProcessType</key>\n\t<string>Interactive</string>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.String(), nil
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// DesktopEntry renders the XDG autostart .desktop file.
func DesktopEntry(exe string) (string, error) {
	if err := checkExe(exe); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("[Desktop Entry]\n")
	b.WriteString("Type=Application\n")
	b.WriteString("Name=bubbles\n")
	b.WriteString("Comment=bubbles desktop notifications\n")
	b.WriteString("Exec=" + desktopQuote(exe) + " " + BackgroundArg + "\n")
	b.WriteString("Terminal=false\n")
	b.WriteString("X-GNOME-Autostart-enabled=true\n")
	return b.String(), nil
}

// desktopQuote quotes one Exec argument per the Desktop Entry specification:
// double quotes around it and a backslash before the characters that stay
// special inside quotes (double quote, backtick, dollar, backslash). Each
// of those backslashes is doubled again because the value is also a string
// escape context. A percent sign is doubled (field codes).
func desktopQuote(arg string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range arg {
		switch r {
		case '"', '`', '$':
			b.WriteString(`\\`)
			b.WriteRune(r)
		case '\\':
			b.WriteString(`\\\\`)
		case '%':
			b.WriteString("%%")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
