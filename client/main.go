// Command beacon-agent is the outbound-only side of the relay: it dials the
// control plane, keeps a single multiplexed tunnel alive and serves whatever
// the server opens on it. It is pure Go with no build tags, so one set of
// sources covers windows, linux and darwin on amd64 and arm64.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/bolchisb/go-beacon/internal/protocol"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
	// a session that lasted this long counts as healthy, so the next failure
	// starts backing off from scratch instead of from the previous ceiling
	healthySession = 30 * time.Second
)

type config struct {
	server string
	id     string
	caFile string
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, showVersion := parseFlags()
	if showVersion {
		fmt.Println(version)
		return
	}

	tlsCfg, err := tlsConfig(cfg.caFile)
	if err != nil {
		slog.Error("tls setup failed", "err", err)
		os.Exit(1)
	}

	hostname, _ := os.Hostname()
	hello := protocol.Hello{
		AgentID:  cfg.id,
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Version:  version,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("agent starting", "id", hello.AgentID, "server", cfg.server,
		"platform", hello.OS+"/"+hello.Arch, "version", version)

	supervise(ctx, cfg, hello, tlsCfg)
	slog.Info("agent stopped")
}

// supervise keeps exactly one session alive, forever. An agent that gives up
// would need someone to log into the machine to restart it, which is the one
// thing the relay exists to avoid.
func supervise(ctx context.Context, cfg config, hello protocol.Hello, tlsCfg *tls.Config) {
	backoff := minBackoff

	for ctx.Err() == nil {
		start := time.Now()
		err := runSession(ctx, cfg, hello, tlsCfg)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("session ended", "err", err)
		}
		if time.Since(start) >= healthySession {
			backoff = minBackoff
		}

		delay := jitter(backoff)
		slog.Info("reconnecting", "in", delay.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		if backoff < maxBackoff {
			backoff = min(backoff*2, maxBackoff)
		}
	}
}

// jitter spreads reconnects over 50-100% of the backoff window. Without it a
// relay restart brings every agent back in the same millisecond.
func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.5 + rand.Float64()/2))
}

func parseFlags() (config, bool) {
	hostname, _ := os.Hostname()

	var cfg config
	var showVersion bool
	flag.StringVar(&cfg.server, "server", env("BEACON_SERVER", "http://127.0.0.1:8080"),
		"relay URL, http:// or https://")
	flag.StringVar(&cfg.id, "id", env("BEACON_AGENT_ID", hostname),
		"agent identity shown in the dashboard")
	flag.StringVar(&cfg.caFile, "ca-file", env("BEACON_CA_FILE", ""),
		"PEM bundle trusted in addition to the system roots (https only)")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	return cfg, showVersion
}

// tlsConfig trusts the system roots, plus an internal CA when one is given.
// Phase 2 adds the client certificate to this same config.
func tlsConfig(caFile string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile == "" {
		return cfg, nil
	}

	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		// windows and darwin can both refuse to export the system pool; an
		// explicit CA file is still usable on its own
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no certificate found in %s", caFile)
	}
	cfg.RootCAs = pool
	return cfg, nil
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
