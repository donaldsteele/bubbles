package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// RenderSystemdUnit renders /etc/systemd/system/bubbles.service. It is kept
// identical to deploy/systemd/bubbles.service for the default spec.
func RenderSystemdUnit(s Spec) (string, error) {
	if strings.ContainsAny(s.DataDir, " \t\n\"'\\%") || strings.ContainsAny(s.Exe, "\n") {
		return "", errors.New("systemd data directory must not contain spaces, quotes, backslashes or percent signs")
	}
	exec := s.Exe
	if strings.ContainsAny(exec, " \t") {
		exec = `"` + exec + `"`
	}
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=bubbles private LAN messaging server\n")
	b.WriteString("Documentation=https://github.com/donaldsteele/bubbles\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n")
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("User=" + LinuxUser + "\n")
	b.WriteString("Group=" + LinuxUser + "\n")
	b.WriteString("ExecStart=" + exec + " " + strings.Join(s.ServeArgs(), " ") + "\n")
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=5\n")
	b.WriteString("NoNewPrivileges=yes\n")
	b.WriteString("ProtectSystem=strict\n")
	b.WriteString("ProtectHome=yes\n")
	b.WriteString("ReadWritePaths=" + s.DataDir + "\n")
	b.WriteString("PrivateTmp=yes\n")
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String(), nil
}

// SystemdInstallCommands are run after the unit file is written, in order.
func SystemdInstallCommands() [][]string {
	return [][]string{
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", SystemdUnitName},
		{"systemctl", "start", SystemdUnitName},
	}
}

// SystemdPrepareCommands create the service user (when missing) and the data
// directory owned by it. userExists tells whether `id -u bubbles` succeeded.
func SystemdPrepareCommands(s Spec, userExists bool) [][]string {
	var cmds [][]string
	if !userExists {
		cmds = append(cmds, []string{"useradd", "--system", "--user-group", "--home-dir", s.DataDir,
			"--no-create-home", "--shell", "/usr/sbin/nologin", LinuxUser})
	}
	cmds = append(cmds, []string{"install", "-d", "-m", "0700", "-o", LinuxUser, "-g", LinuxUser, s.DataDir})
	return cmds
}

type systemdManager struct{ o Options }

func newSystemd(o Options) Manager { return &systemdManager{o: o} }

func (m *systemdManager) Install(ctx context.Context, s Spec) error {
	if !m.o.IsAdmin() {
		return ErrNotAdmin
	}
	unit, err := RenderSystemdUnit(s)
	if err != nil {
		return err
	}
	_, idErr := m.o.Runner.Run(ctx, "id", "-u", LinuxUser)
	if err := runAll(ctx, m.o.Runner, SystemdPrepareCommands(s, idErr == nil)); err != nil {
		return err
	}
	if err := m.o.WriteFile(SystemdUnitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}
	return runAll(ctx, m.o.Runner, SystemdInstallCommands())
}

func (m *systemdManager) Uninstall(ctx context.Context) error {
	if !m.o.IsAdmin() {
		return ErrNotAdmin
	}
	// Stopping or disabling a unit that is already gone is not an error.
	_, _ = m.o.Runner.Run(ctx, "systemctl", "disable", "--now", SystemdUnitName)
	if err := m.o.Remove(SystemdUnitPath); err != nil && !isNotExist(err) {
		return fmt.Errorf("remove unit: %w", err)
	}
	return runAll(ctx, m.o.Runner, [][]string{{"systemctl", "daemon-reload"}})
}

func (m *systemdManager) Start(ctx context.Context) error {
	if !m.o.IsAdmin() {
		return ErrNotAdmin
	}
	return runAll(ctx, m.o.Runner, [][]string{{"systemctl", "start", SystemdUnitName}})
}

func (m *systemdManager) Stop(ctx context.Context) error {
	if !m.o.IsAdmin() {
		return ErrNotAdmin
	}
	return runAll(ctx, m.o.Runner, [][]string{{"systemctl", "stop", SystemdUnitName}})
}

func (m *systemdManager) Status(ctx context.Context) (string, error) {
	out, err := m.o.Runner.Run(ctx, "systemctl", "is-active", SystemdUnitName)
	if out != "" {
		// is-active exits non-zero for inactive/failed units but still prints the state.
		return out, nil
	}
	return "unknown", err
}
