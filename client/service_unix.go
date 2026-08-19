//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const (
	systemdUnit  = "/etc/systemd/system/beacon.service"
	launchdLabel = "com.gobeacon.agent"
	launchdPlist = "/Library/LaunchDaemons/" + launchdLabel + ".plist"
)

// runUnderServiceManager exists only for windows; systemd and launchd run the
// binary as an ordinary foreground process.
func runUnderServiceManager() (bool, error) { return false, nil }

func requireElevation() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("this needs root: try sudo")
	}
	return nil
}

func installPlan(cfg *resolved, target string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "would copy   %s\n", target)
	fmt.Fprintf(&b, "would write  %s\n", configPath())
	if runtime.GOOS == "darwin" {
		fmt.Fprintf(&b, "would write  %s\n\n%s\n", launchdPlist, plistContent(target))
		fmt.Fprintf(&b, "would run    launchctl bootstrap system %s\n", launchdPlist)
		fmt.Fprintf(&b, "would run    launchctl kickstart -k system/%s\n", launchdLabel)
	} else {
		fmt.Fprintf(&b, "would write  %s\n\n%s\n", systemdUnit, unitContent(target))
		b.WriteString("would run    systemctl daemon-reload\n")
		b.WriteString("would run    systemctl enable beacon\n")
		b.WriteString("would run    systemctl start beacon\n")
	}
	fmt.Fprintf(&b, "\nrelay        %s\nagent id     %s\n", cfg.Server, cfg.AgentID)
	return b.String()
}

func serviceInstall(target string) error {
	if runtime.GOOS == "darwin" {
		if err := os.WriteFile(launchdPlist, []byte(plistContent(target)), 0o644); err != nil {
			return err
		}
		// bootout first so a reinstall replaces a stale definition
		run("launchctl", "bootout", "system/"+launchdLabel)
		return run("launchctl", "bootstrap", "system", launchdPlist)
	}

	if err := os.WriteFile(systemdUnit, []byte(unitContent(target)), 0o644); err != nil {
		return err
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	return run("systemctl", "enable", "beacon")
}

func serviceUninstall() error {
	if runtime.GOOS == "darwin" {
		run("launchctl", "bootout", "system/"+launchdLabel)
		if err := os.Remove(launchdPlist); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	run("systemctl", "disable", "--now", "beacon")
	if err := os.Remove(systemdUnit); err != nil && !os.IsNotExist(err) {
		return err
	}
	return run("systemctl", "daemon-reload")
}

func serviceStart() error {
	if runtime.GOOS == "darwin" {
		return run("launchctl", "kickstart", "-k", "system/"+launchdLabel)
	}
	return run("systemctl", "start", "beacon")
}

func serviceStop() error {
	if runtime.GOOS == "darwin" {
		return run("launchctl", "bootout", "system/"+launchdLabel)
	}
	return run("systemctl", "stop", "beacon")
}

func serviceStatus() (svcInfo, error) {
	if runtime.GOOS == "darwin" {
		_, err := os.Stat(launchdPlist)
		if err != nil {
			return svcInfo{}, nil
		}
		running := run("launchctl", "print", "system/"+launchdLabel) == nil
		return svcInfo{Installed: true, Running: running}, nil
	}

	if _, err := os.Stat(systemdUnit); err != nil {
		return svcInfo{}, nil
	}
	return svcInfo{Installed: true, Running: run("systemctl", "is-active", "--quiet", "beacon") == nil}, nil
}

func run(name string, args ...string) error {
	out, err := exec.CommandContext(context.Background(), name, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}
		return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return nil
}

func unitContent(target string) string {
	return `[Unit]
Description=go-beacon agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + target + ` run
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`
}

func plistContent(target string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + launchdLabel + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + target + `</string>
    <string>run</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/var/log/beacon.log</string>
  <key>StandardErrorPath</key><string>/var/log/beacon.log</string>
</dict>
</plist>
`
}
