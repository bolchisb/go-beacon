package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// svcInfo is what the platform service manager can tell us.
type svcInfo struct {
	Installed bool
	Running   bool
}

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("beacon install", flag.ExitOnError)
	fs.String(keyServer, "", "relay URL, http:// or https://")
	fs.String(keyID, "", "agent identity shown in the dashboard")
	fs.String(keyCA, "", "PEM bundle trusted in addition to the system roots")
	dryRun := fs.Bool("dry-run", false, "print what would be done and change nothing")
	fs.Usage = func() {
		usageFor(fs, "beacon install --server URL", "Install the agent as a system service.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadConfig(explicitFlags(fs))
	if err != nil {
		return err
	}
	target := installPath()

	if *dryRun {
		fmt.Print(installPlan(cfg, target))
		return nil
	}

	if err := requireElevation(); err != nil {
		return err
	}
	if err := copyExecutable(target); err != nil {
		return err
	}
	if _, err := saveConfig(cfg.Config); err != nil {
		return err
	}
	if err := serviceInstall(target); err != nil {
		return err
	}
	if err := serviceStart(); err != nil {
		return err
	}

	fmt.Printf("installed %s\nconfig   %s\nrelay    %s\n\nrun `beacon status` to check it\n",
		target, configPath(), cfg.Server)
	return nil
}

func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("beacon uninstall", flag.ExitOnError)
	fs.Usage = func() { usageFor(fs, "beacon uninstall", "Remove the service and the installed binary.") }
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireElevation(); err != nil {
		return err
	}

	if err := serviceUninstall(); err != nil {
		return err
	}
	// the socket outlives a hard kill, so remove it explicitly
	for _, p := range socketPaths() {
		os.Remove(p)
	}
	if err := os.Remove(installPath()); err != nil && !os.IsNotExist(err) {
		return err
	}

	fmt.Printf("service and binary removed\nconfig left at %s\n", configPath())
	return nil
}

func cmdServiceControl(name string, args []string) error {
	fs := flag.NewFlagSet("beacon "+name, flag.ExitOnError)
	fs.Usage = func() { usageFor(fs, "beacon "+name, "Ask the service manager to "+name+" the agent.") }
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireElevation(); err != nil {
		return err
	}

	switch name {
	case "start":
		return serviceStart()
	case "stop":
		return serviceStop()
	default:
		if err := serviceStop(); err != nil {
			return err
		}
		return serviceStart()
	}
}

// copyExecutable puts the running binary somewhere stable. A service must not
// point at whatever directory the operator happened to run the installer from.
func copyExecutable(target string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if selfResolved, err := filepath.EvalSymlinks(self); err == nil {
		self = selfResolved
	}
	if self == target {
		return nil
	}

	data, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	// a running binary cannot be overwritten in place on windows, but it can
	// be renamed out of the way first
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, target+".old"); err != nil {
			return err
		}
		defer os.Remove(target + ".old")
	}
	return os.WriteFile(target, data, 0o755)
}
