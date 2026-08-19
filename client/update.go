package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"runtime"
	"strings"
	"time"
)

// updateRepo is stamped at build time so a fork updates from its own releases.
var updateRepo = "bolchisb/go-beacon"

const (
	releaseTimeout  = 30 * time.Second
	downloadTimeout = 5 * time.Minute
)

func cmdUpdate(args []string) error {
	fs := flag.NewFlagSet("beacon update", flag.ExitOnError)
	check := fs.Bool("check", false, "report the available version and change nothing")
	force := fs.Bool("force", false, "reinstall even if the version matches, and restart with streams open")
	fs.Usage = func() {
		usageFor(fs, "beacon update", "Replace this binary with the latest release and restart the agent.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// a previous update on windows cannot delete its own predecessor
	os.Remove(exe + ".old")

	latest, err := latestTag()
	if err != nil {
		return err
	}

	if *check {
		if latest == version {
			p := resultPanel("update", markDone, styOK, "UP TO DATE", "")
			p.kv("version", version)
			p.show()
			return nil
		}
		p := resultPanel("update", markWarn, styWarn, "UPDATE AVAILABLE", "")
		p.kv("running", version)
		p.kv("latest", latest)
		p.footer = "beacon update to apply it"
		p.show()
		return nil
	}
	if latest == version && !*force {
		p := resultPanel("update", markDone, styOK, "UP TO DATE", "")
		p.kv("version", version)
		p.footer = "beacon update --force to reinstall anyway"
		p.show()
		return nil
	}

	if err := guardActiveStreams(*force); err != nil {
		return err
	}

	step("downloading %s (%s)", latest, assetName())
	blob, err := download(assetURL(latest, assetName()))
	if err != nil {
		return err
	}
	step("verifying checksum")
	if err := verifyChecksum(latest, blob); err != nil {
		return err
	}
	step("replacing binary")
	if err := replaceBinary(exe, blob); err != nil {
		return err
	}
	restarted := restartAfterUpdate()

	p := resultPanel("update", markDone, styOK, "UPDATED", "")
	p.kv("from", version)
	p.kv("to", latest)
	p.kv("binary", exe)
	p.kv("size", humanBytes(uint64(len(blob))))
	p.kv("agent", restarted)
	p.footer = "beacon status"
	p.show()
	return nil
}

// latestTag reads the version from the redirect GitHub serves for
// /releases/latest. The REST API would be the obvious choice, but it allows 60
// requests an hour per IP and conditional requests still spend that budget, so
// a fleet behind one NAT would exhaust it.
func latestTag() (string, error) {
	client := &http.Client{
		Timeout: releaseTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	url := fmt.Sprintf("https://github.com/%s/releases/latest", updateRepo)
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	location := resp.Header.Get("Location")
	if location == "" {
		return "", fmt.Errorf("%s: no redirect to a release, got %s", url, resp.Status)
	}
	tag := path.Base(location)
	if tag == "" || tag == "releases" {
		return "", fmt.Errorf("cannot read a tag out of %q", location)
	}
	return tag, nil
}

func assetName() string {
	name := fmt.Sprintf("beacon-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func assetURL(tag, name string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", updateRepo, tag, name)
}

func download(url string) ([]byte, error) {
	client := &http.Client{Timeout: downloadTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// verifyChecksum catches a truncated or corrupted download. It is not a
// signature: SHA256SUMS ships from the same release, so anyone able to replace
// the binary can replace the checksum with it.
func verifyChecksum(tag string, blob []byte) error {
	sums, err := download(assetURL(tag, "SHA256SUMS"))
	if err != nil {
		return fmt.Errorf("checksums: %w", err)
	}

	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		digest, name, ok := strings.Cut(strings.TrimSpace(line), "  ")
		if ok && name == assetName() {
			want = digest
			break
		}
	}
	if want == "" {
		return fmt.Errorf("SHA256SUMS has no entry for %s", assetName())
	}

	sum := sha256.Sum256(blob)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("checksum mismatch: got %s, expected %s", got, want)
	}
	return nil
}

// replaceBinary writes the new build beside the old one and renames it into
// place. A running executable cannot be written to, but it can be renamed
// over, and the rename is atomic: a failed download never leaves a half binary
// where the working one used to be.
func replaceBinary(exe string, blob []byte) error {
	staged := exe + ".new"
	if err := os.WriteFile(staged, blob, 0o755); err != nil {
		return elevationHint(exe, err)
	}
	// WriteFile keeps the mode of an existing file, so set it explicitly
	if err := os.Chmod(staged, 0o755); err != nil {
		os.Remove(staged)
		return err
	}

	if runtime.GOOS == "windows" {
		// windows refuses to replace a running image, but it allows moving it
		if err := os.Rename(exe, exe+".old"); err != nil {
			os.Remove(staged)
			return err
		}
	}
	if err := os.Rename(staged, exe); err != nil {
		os.Remove(staged)
		return elevationHint(exe, err)
	}
	return nil
}

func elevationHint(exe string, err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("cannot replace %s: permission denied, try sudo", exe)
	}
	return err
}

// guardActiveStreams stops an update from cutting a session someone is working
// in. This is a jump host: a restart at the wrong moment drops a live shell.
func guardActiveStreams(force bool) error {
	st, _, err := fetchStatus()
	if err != nil || st.Streams == 0 || force {
		return nil
	}
	return fmt.Errorf("%d stream(s) in use right now; wait, or pass --force to interrupt them", st.Streams)
}

// restartAfterUpdate returns what happened to the running agent, so the panel
// can say it rather than leaving the operator to guess.
func restartAfterUpdate() string {
	svc, err := serviceStatus()
	if err == nil && svc.Installed && svc.Running {
		step("restarting service")
		if err := serviceStop(); err != nil {
			return "restart failed: " + err.Error()
		}
		if err := serviceStart(); err != nil {
			return "restart failed: " + err.Error()
		}
		if settled(true).Running {
			return "service restarted"
		}
		return "service did not come back, check its log"
	}

	if _, _, err := fetchStatus(); err == nil {
		// an agent answers but no service owns it: someone started it by hand
		return "running in the foreground, restart it yourself"
	}
	return "not running"
}
