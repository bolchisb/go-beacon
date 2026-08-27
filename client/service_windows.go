//go:build windows

package main

import (
	"context"
	"fmt"
	"github.com/bolchisb/go-beacon/internal/supervise"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	winServiceName = "beacon"
	winDisplayName = "go-beacon agent"
	winDescription = "Maintains the outbound tunnel to the go-beacon relay."
)

// runUnderServiceManager detects being launched by the Service Control Manager
// and, when that is the case, runs the agent through the SCM protocol instead
// of as a console program.
func runUnderServiceManager() (bool, error) {
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		return false, err
	}

	// A windows service has nowhere to write: systemd captures stdout on linux
	// and launchd redirects it on darwin, but here it is discarded. Without a
	// file, the only way to learn why the agent refused a connection is to
	// guess.
	if f, err := openServiceLog(); err == nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})))
	}

	return true, svc.Run(winServiceName, &windowsService{})
}

// maxServiceLogBytes bounds the log. It is checked once, at startup, which is
// enough for a process that logs a line per session and restarts on update.
const maxServiceLogBytes = 5 << 20

func serviceLogPath() string {
	return filepath.Join(filepath.Dir(configPath()), "beacon.log")
}

func openServiceLog() (*os.File, error) {
	path := serviceLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err == nil && info.Size() > maxServiceLogBytes {
		os.Remove(path + ".1")
		os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

type windowsService struct{}

func (windowsService) Execute(_ []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	s <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	supervise.Go("windows-service", func() {
		defer close(done)
		cfg, err := loadConfig(nil)
		if err != nil {
			return
		}
		runAgent(ctx, cfg)
	})

	s <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case <-done:
			return false, 0
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
				}
				return false, 0
			}
		}
	}
}

func requireElevation() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("this needs an elevated prompt: %w", err)
	}
	m.Disconnect()
	return nil
}

func installPlan(cfg *resolved, target string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "would copy     %s\n", target)
	fmt.Fprintf(&b, "would write    %s\n", configPath())
	fmt.Fprintf(&b, "would register service %q (%s)\n", winServiceName, winDisplayName)
	fmt.Fprintf(&b, "would log to   %s\n", serviceLogPath())
	fmt.Fprintf(&b, "               start type automatic, restart after 5s on failure\n")
	fmt.Fprintf(&b, "               command: %s run\n", target)
	fmt.Fprintf(&b, "\nrelay          %s\nagent id       %s\n", cfg.Server, cfg.AgentID)
	return b.String()
}

// runAsAccount and runAsPassword are set by `beacon install --run-as`. Empty
// means LocalSystem, which is the default and the right one for a machine
// nobody sits at.
var runAsAccount, runAsPassword string

func serviceInstall(target string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	// replace a previous definition rather than failing on reinstall
	if existing, err := m.OpenService(winServiceName); err == nil {
		existing.Control(svc.Stop)
		existing.Delete()
		existing.Close()
	}

	cfg := mgr.Config{
		DisplayName:  winDisplayName,
		Description:  winDescription,
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	}
	if runAsAccount != "" {
		cfg.ServiceStartName = runAsAccount
		cfg.Password = runAsPassword
	}

	s, err := m.CreateService(winServiceName, target, cfg, "run")
	if err != nil {
		if runAsAccount != "" {
			// The usual cause, and it is not something this can fix from here:
			// granting SeServiceLogonRight needs the LSA policy APIs. Saying
			// exactly which right is missing beats a wrapped Win32 error.
			return fmt.Errorf("registering the service as %s failed: %w\n\n"+
				"If this is a logon-rights problem, grant the account the "+
				"\"Log on as a service\" right:\n"+
				"  secpol.msc -> Local Policies -> User Rights Assignment -> Log on as a service",
				runAsAccount, err)
		}
		return err
	}
	defer s.Close()

	// the agent is the only way back into the machine, so a crash must not
	// leave it stopped
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
	}, 86400); err != nil {
		return err
	}

	// By default the SCM only reacts when a service dies without reporting
	// SERVICE_STOPPED. This makes a clean exit with a non-zero code count too,
	// which is the difference between a graceful self-restart and pretending to
	// crash. Microsoft documents it as taking effect at the next boot, so it is
	// a safety net for later rather than something to rely on today.
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		slog.Warn("could not enable recovery on clean failure", "err", err)
	}
	return nil
}

func serviceUninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	s, err := m.OpenService(winServiceName)
	if err != nil {
		return nil // already gone
	}
	defer s.Close()

	s.Control(svc.Stop)
	return s.Delete()
}

func serviceStart() error {
	return withService(func(s *mgr.Service) error { return s.Start("run") })
}

func serviceStop() error {
	return withService(func(s *mgr.Service) error {
		_, err := s.Control(svc.Stop)
		return err
	})
}

func serviceStatus() (svcInfo, error) {
	m, err := mgr.Connect()
	if err != nil {
		return svcInfo{}, err
	}
	defer m.Disconnect()

	s, err := m.OpenService(winServiceName)
	if err != nil {
		return svcInfo{}, nil
	}
	defer s.Close()

	st, err := s.Query()
	if err != nil {
		return svcInfo{Installed: true}, err
	}
	return svcInfo{Installed: true, Running: st.State == svc.Running}, nil
}

func withService(fn func(*mgr.Service) error) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	s, err := m.OpenService(winServiceName)
	if err != nil {
		return fmt.Errorf("service %q is not installed", winServiceName)
	}
	defer s.Close()
	return fn(s)
}
