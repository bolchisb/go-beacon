package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// errSilent ends a command with a non-zero status after its panel has already
// explained what went wrong, so the failure is not reported twice.
var errSilent = errors.New("")

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
	fs.String(keyUser, "", "operator username to enrol this machine with")
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

	// Enrolment first: it needs a human, and failing here should leave the
	// machine untouched rather than half installed.
	if err := enrollThisMachine(cfg); err != nil {
		return err
	}

	before, _ := serviceStatus()

	step("installing binary")
	if err := copyExecutable(target); err != nil {
		return err
	}
	step("writing config")
	if _, err := saveConfig(cfg.Config); err != nil {
		return err
	}
	step("registering service")
	if err := serviceInstall(target); err != nil {
		return err
	}
	step("starting service")
	if err := serviceStart(); err != nil {
		return err
	}

	state := "INSTALLED"
	if before.Installed {
		state = "REINSTALLED"
	}
	p := resultPanel("install", markDone, styOK, state, "")
	p.kv("binary", target)
	p.kv("config", configPath())
	p.kv("relay", cfg.Server)
	p.kv("agent id", cfg.AgentID)
	p.kv("enrolled", "yes")
	p.kv("service", serviceStateText(settled(true)))
	p.footer = "beacon status"
	p.show()
	return nil
}

// settled waits briefly for the service manager to reach the state we asked
// for, so the panel reports what actually happened rather than what was
// requested. Reporting success on a service that failed to come up would be
// worse than reporting nothing.
func settled(wantRunning bool) svcInfo {
	var info svcInfo
	for i := 0; i < 20; i++ {
		var err error
		if info, err = serviceStatus(); err == nil && info.Running == wantRunning {
			return info
		}
		time.Sleep(100 * time.Millisecond)
	}
	return info
}

func serviceStateText(info svcInfo) string {
	switch {
	case !info.Installed:
		return "not installed"
	case info.Running:
		return "running"
	default:
		return "registered, not running"
	}
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

	p := resultPanel("uninstall", markDone, styOK, "REMOVED", "")
	p.kv("service", "unregistered")
	p.kv("binary", installPath())
	p.kv("config", "kept at "+configPath())
	p.footer = "beacon install to set it up again"
	p.show()
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

	before, err := serviceStatus()
	if err != nil {
		return err
	}
	if !before.Installed {
		p := resultPanel(name, markFail, styErr, "NOT INSTALLED", "")
		p.kv("install", "beacon install --server https://relay.example.com")
		p.show()
		return errSilent
	}

	switch {
	case name == "start" && before.Running:
		p := resultPanel(name, markLive, styOK, "ALREADY RUNNING", "")
		p.kv("service", "running")
		p.footer = "beacon status"
		p.show()
		return nil

	case name == "stop" && !before.Running:
		p := resultPanel(name, markIdle, styDim, "ALREADY STOPPED", "")
		p.kv("service", "registered, not running")
		p.show()
		return nil

	case name == "start":
		step("starting service")
		if err := serviceStart(); err != nil {
			return err
		}

	case name == "stop":
		step("stopping service")
		if err := serviceStop(); err != nil {
			return err
		}

	default:
		step("stopping service")
		if before.Running {
			if err := serviceStop(); err != nil {
				return err
			}
		}
		step("starting service")
		if err := serviceStart(); err != nil {
			return err
		}
	}

	wantRunning := name != "stop"
	after := settled(wantRunning)
	if after.Running != wantRunning {
		p := resultPanel(name, markWarn, styWarn, "UNCONFIRMED", "")
		p.kv("service", serviceStateText(after))
		p.kv("hint", "check the service log")
		p.show()
		return errSilent
	}

	state := map[string]string{"start": "STARTED", "stop": "STOPPED", "restart": "RESTARTED"}[name]
	p := resultPanel(name, markDone, styOK, state, "")
	p.kv("service", serviceStateText(after))
	if wantRunning {
		p.footer = "beacon status"
	}
	p.show()
	return nil
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

// enrollThisMachine generates this machine's keypair if it has none and trades
// an operator's credentials for an assertion binding it to this agent id.
//
// The credentials are typed here, used for one request, and dropped. What stays
// on the machine is its own private key and the assertion -- an identity for
// this agent and nothing that reaches any other.
func enrollThisMachine(cfg *resolved) error {
	priv, err := ensureKeypair(&cfg.Config)
	if err != nil {
		return err
	}

	if cfg.Assertion != "" {
		fmt.Println("  already enrolled; re-enrolling to refresh the assertion")
	}

	username := cfg.Username
	if username == "" {
		fmt.Printf("\nEnrolling %s with %s\n", cfg.AgentID, cfg.Server)
		if username, err = prompt("Operator username: "); err != nil {
			return err
		}
	}
	password, err := promptPassword("Operator password: ")
	if err != nil {
		return err
	}

	step("enrolling with the relay")
	assertion, err := enroll(cfg.Server, cfg.CAFile, username, password,
		cfg.AgentID, publicKeyOf(priv))
	if err != nil {
		return fmt.Errorf("enrolment failed: %w", err)
	}
	cfg.Assertion = assertion
	cfg.Username = username
	return nil
}
