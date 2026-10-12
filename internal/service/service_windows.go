//go:build windows

package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func isAdmin() bool { return windows.GetCurrentProcessToken().IsElevated() }

func platformManager(o Options) Manager { return &winManager{o: o} }

type winManager struct{ o Options }

func (m *winManager) connect() (*mgr.Mgr, error) {
	c, err := mgr.Connect()
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil, ErrNotAdmin
		}
		return nil, fmt.Errorf("connect to service manager: %w", err)
	}
	return c, nil
}

func (m *winManager) Install(ctx context.Context, s Spec) error {
	if !m.o.IsAdmin() {
		return ErrNotAdmin
	}
	httpsPort, httpPort, err := s.Ports()
	if err != nil {
		return err
	}
	c, err := m.connect()
	if err != nil {
		return err
	}
	defer c.Disconnect()
	svcH, err := c.CreateService(WindowsServiceName, s.Exe, mgr.Config{
		DisplayName: "bubbles",
		Description: "Private LAN messaging server",
		StartType:   mgr.StartAutomatic,
	}, s.ServeArgs()...)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer svcH.Close()
	// Restart after crashes, e.g. when the LAN is not up yet at boot.
	_ = svcH.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: time.Minute},
	}, 86400)
	if err := addFirewallRules(ctx, m.o.Runner, httpsPort, httpPort); err != nil {
		removeFirewallRules(ctx, m.o.Runner)
		return rollbackCreated(svcH.Delete, err)
	}
	if err := svcH.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	return nil
}

// rollbackCreated deletes a service created by a failed Install so that a
// retry starts clean. A delete failure is joined onto cause.
func rollbackCreated(del func() error, cause error) error {
	if derr := del(); derr != nil {
		return errors.Join(cause, fmt.Errorf("rollback: delete service: %w", derr))
	}
	return cause
}

func (m *winManager) open(name string) (*mgr.Mgr, *mgr.Service, error) {
	if !m.o.IsAdmin() {
		return nil, nil, ErrNotAdmin
	}
	c, err := m.connect()
	if err != nil {
		return nil, nil, err
	}
	s, err := c.OpenService(name)
	if err != nil {
		_ = c.Disconnect()
		return nil, nil, fmt.Errorf("open service %q (is it installed?): %w", name, err)
	}
	return c, s, nil
}

func (m *winManager) Uninstall(ctx context.Context) error {
	c, s, err := m.open(WindowsServiceName)
	if err != nil {
		return err
	}
	defer c.Disconnect()
	defer s.Close()
	_ = stopAndWait(s)
	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	removeFirewallRules(ctx, m.o.Runner)
	return nil
}

func (m *winManager) Start(ctx context.Context) error {
	c, s, err := m.open(WindowsServiceName)
	if err != nil {
		return err
	}
	defer c.Disconnect()
	defer s.Close()
	if err := s.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	return nil
}

func (m *winManager) Stop(ctx context.Context) error {
	c, s, err := m.open(WindowsServiceName)
	if err != nil {
		return err
	}
	defer c.Disconnect()
	defer s.Close()
	return stopAndWait(s)
}

func stopAndWait(s *mgr.Service) error {
	st, err := s.Control(svc.Stop)
	if err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for st.State != svc.Stopped {
		if time.Now().After(deadline) {
			return errors.New("stop service: timed out")
		}
		time.Sleep(300 * time.Millisecond)
		if st, err = s.Query(); err != nil {
			return fmt.Errorf("query service: %w", err)
		}
	}
	return nil
}

func (m *winManager) Status(ctx context.Context) (string, error) {
	c, err := m.connect()
	if err != nil {
		return "", err
	}
	defer c.Disconnect()
	s, err := c.OpenService(WindowsServiceName)
	if err != nil {
		return "not installed", nil
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return "", fmt.Errorf("query service: %w", err)
	}
	return stateName(st.State), nil
}

func stateName(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start-pending"
	case svc.StopPending:
		return "stop-pending"
	case svc.Running:
		return "running"
	case svc.Paused:
		return "paused"
	}
	return "unknown"
}

// IsWindowsService reports whether the process was started by the SCM.
func IsWindowsService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

// RunAsService runs run under the SCM handler. Stop and Shutdown requests
// cancel the context passed to run and wait for it to return (graceful
// shutdown); the returned code becomes the service's exit code.
func RunAsService(run func(ctx context.Context) int) error {
	return svc.Run(WindowsServiceName, &handler{run: run})
}

type handler struct{ run func(ctx context.Context) int }

func (h *handler) Execute(_ []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	s <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- h.run(ctx) }()
	s <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending, WaitHint: 20000}
				cancel()
				return exitStatus(<-done)
			}
		case code := <-done:
			s <- svc.Status{State: svc.StopPending}
			return exitStatus(code)
		}
	}
}

func exitStatus(code int) (bool, uint32) {
	if code == 0 {
		return false, 0
	}
	return true, uint32(code)
}
