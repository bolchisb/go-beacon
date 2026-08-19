package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	// how long a freshly restarted agent has to answer its control socket
	// before the update is treated as bad
	verifyTimeout = 3 * time.Minute
	verifyPoll    = 3 * time.Second

	autoUpdateInterval = time.Hour
	// the helper waits for its parent to get out of the way before touching
	// the service
	helperGrace = 2 * time.Second
)

// updateTarget is the binary that actually matters. `beacon update` run from a
// copy in a downloads folder would otherwise replace that copy and leave the
// service on the old build, reporting success the whole time.
func updateTarget() (string, error) {
	if svc, err := serviceStatus(); err == nil && svc.Installed {
		return installPath(), nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// isServiceInstance reports whether this process is the installed agent rather
// than a copy someone is running by hand. Only the installed one may update
// itself; a developer running `beacon run` should not have the binary swapped
// underneath them.
func isServiceInstance() bool {
	svc, err := serviceStatus()
	if err != nil || !svc.Installed {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return sameFile(exe, installPath())
}

func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

// restartAndVerify puts the new binary into service and makes sure it works. If
// the agent does not come back, the previous binary is restored: a build that
// starts and never answers would otherwise leave a machine that can only be
// reached through the very tunnel it is failing to open.
func restartAndVerify(target string) error {
	if err := serviceStop(); err != nil {
		slog.Warn("update: stopping the service failed", "err", err)
	}
	if err := serviceStart(); err != nil {
		if rollbackErr := rollback(target); rollbackErr != nil {
			return fmt.Errorf("the new binary would not start (%v) and the previous one could not be restored: %w", err, rollbackErr)
		}
		return fmt.Errorf("the new binary would not start, previous one restored: %w", err)
	}

	if waitForAgent(verifyTimeout) {
		return nil
	}

	slog.Warn("update: the new binary never answered, rolling back")
	if err := rollback(target); err != nil {
		return fmt.Errorf("the new binary never answered and the previous one could not be restored: %w", err)
	}
	if err := serviceStart(); err != nil {
		return fmt.Errorf("rolled back, but the service would not start: %w", err)
	}
	return errors.New("the new binary never answered; the previous one was restored")
}

// waitForAgent asks only that the agent answer, not that it be connected. A
// relay that happens to be down would otherwise look like a bad build and get
// a working update rolled back.
func waitForAgent(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, _, err := fetchStatus(); err == nil {
			return true
		}
		time.Sleep(verifyPoll)
	}
	return false
}

func rollback(target string) error {
	previous := target + ".old"
	if _, err := os.Stat(previous); err != nil {
		return fmt.Errorf("no previous binary at %s: %w", previous, err)
	}
	if err := serviceStop(); err != nil {
		slog.Warn("rollback: stopping the service failed", "err", err)
	}
	// keep the bad build rather than deleting it; it is the only evidence
	os.Remove(target + ".failed")
	if err := os.Rename(target, target+".failed"); err != nil {
		return err
	}
	return os.Rename(previous, target)
}

// spawnRestartHelper hands the restart to a separate process. A service cannot
// restart itself: asking the service manager to stop it means asking it to stop
// the caller, mid-call. The helper outlives that, which is also the only place
// a rollback can run from.
func spawnRestartHelper(target string) error {
	cmd := exec.Command(target, "restart-after-update", "--target", target)
	cmd.SysProcAttr = detachedProcess()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	// let it go; this process is about to be stopped by it
	return cmd.Process.Release()
}

// cmdRestartAfterUpdate is the helper. It is not meant to be run by hand.
func cmdRestartAfterUpdate(args []string) error {
	fs := flag.NewFlagSet("beacon restart-after-update", flag.ExitOnError)
	target := fs.String("target", "", "binary that was replaced")
	fs.Usage = func() {
		usageFor(fs, "beacon restart-after-update", "Internal: restart the service after an update and roll back if it fails.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *target == "" {
		return errors.New("--target is required")
	}

	// give the process that spawned us time to return before we stop it
	time.Sleep(helperGrace)
	return restartAndVerify(*target)
}

// autoUpdateLoop checks for a new release once an hour. The interval is
// jittered so a fleet does not arrive at GitHub in the same second after a
// release, and an update is skipped while anyone is working through the agent.
func autoUpdateLoop(ctx context.Context, target string) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitterAround(autoUpdateInterval)):
		}

		if err := autoUpdateOnce(target); err != nil {
			slog.Warn("auto-update", "err", err)
		}
	}
}

func autoUpdateOnce(target string) error {
	latest, err := latestTag()
	if err != nil {
		return err
	}
	if latest == version {
		return nil
	}

	if err := guardActiveStreams(false); err != nil {
		slog.Info("auto-update: postponed", "reason", err)
		return nil
	}

	slog.Info("auto-update: applying", "from", version, "to", latest)
	blob, err := download(assetURL(latest, assetName()))
	if err != nil {
		return err
	}
	if err := verifyChecksum(latest, blob); err != nil {
		return err
	}
	if err := replaceBinary(target, blob); err != nil {
		return err
	}
	// from here the restart, the verification and any rollback belong to a
	// process that is not about to be stopped
	return spawnRestartHelper(target)
}

// jitterAround spreads a periodic action over 75-125% of its interval.
func jitterAround(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.75 + rand.Float64()/2))
}
