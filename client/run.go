package main

import (
	"context"
	"crypto/ed25519"
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
	"github.com/bolchisb/go-beacon/internal/supervise"
)

const (
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
	// a session that lasted this long counts as healthy, so the next failure
	// starts backing off from scratch instead of from the previous ceiling
	healthySession = 30 * time.Second
)

// shipper forwards agent warnings to the relay so they show up in the
// dashboard. It is package level because the logger is.
var shipper *logHandler

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
	// The key is loaded, never generated here: an agent that minted its own
	// identity at runtime would be enrolling itself, which is the opposite of
	// the point. `beacon install` is where enrolment happens.
	priv, err := ensureKeypair(&cfg.Config)
	if err != nil {
		return err
	}
	if cfg.Assertion == "" {
		return fmt.Errorf("this agent is not enrolled with %s: run `beacon install` again", cfg.Server)
	}

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

	// services must be settled before any stream can arrive
	setServices(cfg.Services)

	configPath := ""
	if cfg.exists {
		configPath = cfg.path
	}
	st := newAgentState(cfg.AgentID, cfg.Server, configPath)

	// from here the agent's own warnings reach the dashboard, which is the only
	// place an operator can see them on a machine they cannot log into
	shipper = newLogHandler(slog.Default().Handler())
	slog.SetDefault(slog.New(shipper))

	sock, sockErr := serveControl(ctx, st)
	if sockErr != nil {
		// the agent is still fully functional, only `beacon status` is blind
		slog.Warn("control socket unavailable", "err", sockErr)
	} else {
		slog.Info("control socket ready", "path", sock)
	}

	// run by hand, say what is about to happen; run by a service manager,
	// keep the log free of decoration
	if isInteractive() {
		p := resultPanel("run", markLive, styOK, "STARTING", "")
		p.kv("agent", cfg.AgentID)
		p.kv("relay", cfg.Server)
		p.kv("platform", hello.OS+"/"+hello.Arch)
		if configPath != "" {
			p.kv("config", configPath)
		}
		if sockErr == nil {
			p.kv("socket", sock)
		}
		p.footer = "ctrl-c to stop"
		p.show()
	}

	// Only the installed agent updates itself. A developer running `beacon run`
	// by hand must not have the binary swapped underneath them.
	if cfg.autoUpdate() && isServiceInstance() {
		target, err := updateTarget()
		if err == nil {
			slog.Info("auto-update enabled", "every", autoUpdateInterval, "binary", target)
			supervise.Go("auto-update", func() { autoUpdateLoop(ctx, target) })
		}
	}

	slog.Info("agent starting", "id", hello.AgentID, "server", cfg.Server,
		"platform", hello.OS+"/"+hello.Arch, "version", version)

	superviseSessions(ctx, cfg, hello, tlsCfg, st, priv)
	slog.Info("agent stopped")
	return nil
}

// superviseSessions keeps exactly one session alive, forever. An agent that gives up
// would need someone to log into the machine to restart it, which is the one
// thing the relay exists to avoid.
func superviseSessions(ctx context.Context, cfg *resolved, hello protocol.Hello, tlsCfg *tls.Config, st *agentState, priv ed25519.PrivateKey) {
	server := cfg.Server
	backoff := minBackoff

	for ctx.Err() == nil {
		start := time.Now()
		st.connecting()
		// A panic inside a session becomes an ordinary session failure: the
		// loop below already knows how to back off and try again, and an agent
		// that killed itself over one bad frame would need someone to walk to
		// the machine.
		err := supervise.Do("session", func() error {
			// Fetched fresh each attempt: the challenge is single use, so a
			// reconnect cannot reuse the last one.
			identity, err := identityHeaders(cfg, priv)
			if err != nil {
				return err
			}
			return runSession(ctx, server, hello, tlsCfg, st, identity)
		})
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
