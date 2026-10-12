package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

// LaunchdLogPath receives the daemon's stdout and stderr.
const LaunchdLogPath = "/var/log/bubbles.log"

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// RenderLaunchdPlist renders /Library/LaunchDaemons/com.bubbles.server.plist.
// It is kept identical to deploy/launchd/com.bubbles.server.plist for the
// default spec.
func RenderLaunchdPlist(s Spec) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	b.WriteString("\t<key>Label</key>\n\t<string>" + LaunchdLabel + "</string>\n")
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	b.WriteString("\t\t<string>" + xmlEscape(s.Exe) + "</string>\n")
	for _, a := range s.ServeArgs() {
		b.WriteString("\t\t<string>" + xmlEscape(a) + "</string>\n")
	}
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>WorkingDirectory</key>\n\t<string>" + xmlEscape(s.DataDir) + "</string>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n")
	b.WriteString("\t<key>StandardOutPath</key>\n\t<string>" + LaunchdLogPath + "</string>\n")
	b.WriteString("\t<key>StandardErrorPath</key>\n\t<string>" + LaunchdLogPath + "</string>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// Launchd command lines.
var (
	launchdTarget = "system/" + LaunchdLabel

	LaunchdBootstrap = []string{"launchctl", "bootstrap", "system", LaunchdPlistPath}
	LaunchdBootout   = []string{"launchctl", "bootout", launchdTarget}
	LaunchdKickstart = []string{"launchctl", "kickstart", "-k", launchdTarget}
	LaunchdPrint     = []string{"launchctl", "print", launchdTarget}
)

type launchdManager struct{ o Options }

func newLaunchd(o Options) Manager { return &launchdManager{o: o} }

func (m *launchdManager) Install(ctx context.Context, s Spec) error {
	if !m.o.IsAdmin() {
		return ErrNotAdmin
	}
	if err := runAll(ctx, m.o.Runner, [][]string{{"install", "-d", "-m", "0700", s.DataDir}}); err != nil {
		return err
	}
	if err := m.o.WriteFile(LaunchdPlistPath, []byte(RenderLaunchdPlist(s)), 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	// A previous registration would make bootstrap fail; ignore its absence.
	_, _ = m.o.Runner.Run(ctx, LaunchdBootout[0], LaunchdBootout[1:]...)
	return runAll(ctx, m.o.Runner, [][]string{LaunchdBootstrap})
}

func (m *launchdManager) Uninstall(ctx context.Context) error {
	if !m.o.IsAdmin() {
		return ErrNotAdmin
	}
	_, _ = m.o.Runner.Run(ctx, LaunchdBootout[0], LaunchdBootout[1:]...)
	if err := m.o.Remove(LaunchdPlistPath); err != nil && !isNotExist(err) {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}

func (m *launchdManager) Start(ctx context.Context) error {
	if !m.o.IsAdmin() {
		return ErrNotAdmin
	}
	if err := runAll(ctx, m.o.Runner, [][]string{LaunchdBootstrap}); err == nil {
		return nil
	}
	// Already loaded: restart it.
	return runAll(ctx, m.o.Runner, [][]string{LaunchdKickstart})
}

// Stop unloads the job; with KeepAlive a plain kill would only restart it.
func (m *launchdManager) Stop(ctx context.Context) error {
	if !m.o.IsAdmin() {
		return ErrNotAdmin
	}
	return runAll(ctx, m.o.Runner, [][]string{LaunchdBootout})
}

func (m *launchdManager) Status(ctx context.Context) (string, error) {
	out, err := m.o.Runner.Run(ctx, LaunchdPrint[0], LaunchdPrint[1:]...)
	if err != nil {
		return "stopped", nil
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "state = ") {
			return strings.TrimPrefix(line, "state = "), nil
		}
	}
	return "loaded", nil
}

func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
