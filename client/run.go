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
	"syscall"
	"time"

	"github.com/bolchisb/go-beacon/internal/protocol"
)

const (
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
	// a session that lasted this long counts as healthy, so the next failure
	// starts backing off from scratch instead of from the previous ceiling
	healthySession = 30 * time.Second
)

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("beacon run", flag.ExitOnError)
	fs.String(keyServer, "", "relay URL, http:// or https://")
	fs.String(keyID, "", "agent identity shown in the dashboard")
	fs.String(keyCA, "", "PEM bundle trusted in addition to the system roots")
	fs.Usage = func() { usageFor(fs, "beacon run", "Run the agent in the foreground.") }
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadConfig(explicitFlags(fs))
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return runAgent(ctx, cfg)
}

// runAgent is the whole agent: identity, control socket, and the supervised
// tunnel. The service entry points call straight into it.
func runAgent(ctx context.Context, cfg *resolved) error {
	tlsCfg, err := tlsConfig(cfg.CAFile)
	if err != nil {
		return err
	}

	hostname, _ := os.Hostname()
	hello := protocol.Hello{
		AgentID:  cfg.AgentID,
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Version:  version,
	}

	configPath := ""
	if cfg.exists {
		configPath = cfg.path
	}
	st := newAgentState(cfg.AgentID, cfg.Server, configPath)

	if sock, err := serveControl(ctx, st); err != nil {
		// the agent is still fully functional, only `beacon status` is blind
		slog.Warn("control socket unavailable", "err", err)
	} else {
		slog.Info("control socket ready", "path", sock)
	}

	slog.Info("agent starting", "id", hello.AgentID, "server", cfg.Server,
		"platform", hello.OS+"/"+hello.Arch, "version", version)

	supervise(ctx, cfg.Server, hello, tlsCfg, st)
	slog.Info("agent stopped")
	return nil
}

// supervise keeps exactly one session alive, forever. An agent that gives up
// would need someone to log into the machine to restart it, which is the one
// thing the relay exists to avoid.
func supervise(ctx context.Context, server string, hello protocol.Hello, tlsCfg *tls.Config, st *agentState) {
	backoff := minBackoff

	for ctx.Err() == nil {
		start := time.Now()
		err := runSession(ctx, server, hello, tlsCfg, st)
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
		st.disconnected(err, time.Now().Add(delay))
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

// tlsConfig trusts the system roots, plus an internal CA when one is given.
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

func explicitFlags(fs *flag.FlagSet) map[string]string {
	out := map[string]string{}
	fs.Visit(func(f *flag.Flag) { out[f.Name] = f.Value.String() })
	return out
}
