package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Config is read from the environment so the same binary works under compose
// and under k3s without a config file.
type Config struct {
	Listen string // BEACON_LISTEN, e.g. ":8080"

	// AdminToken gates the dashboard, the API and the MCP endpoint.
	// BEACON_ADMIN_TOKEN_FILE, or BEACON_ADMIN_TOKEN; empty leaves them open,
	// which is announced at startup.
	AdminToken string

	// StateDir is the one writable path the relay has. It holds no secrets --
	// only the cached transit public keys, which is what lets agent
	// verification survive a Vault outage.
	StateDir string // BEACON_STATE_DIR

	// Vault issues and signs agent credentials. Either VaultToken or the
	// AppRole pair authenticates the relay to it; compose uses a token in
	// development and AppRole in production.
	VaultAddr string // BEACON_VAULT_ADDR
	// VaultTokenFile is the Vault Agent token sink. Preferred over the two
	// below: the relay then never holds a long-lived credential, and the agent
	// rotates the token underneath it.
	VaultTokenFile  string // BEACON_VAULT_TOKEN_FILE
	VaultToken      string // BEACON_VAULT_TOKEN
	VaultRoleID     string // BEACON_VAULT_ROLE_ID
	VaultSecretID   string // BEACON_VAULT_SECRET_ID
	VaultTransitKey string // BEACON_VAULT_TRANSIT_KEY

	// TrustedProxies names the peers whose X-Forwarded-Proto header the relay
	// believes: addresses or CIDR blocks, comma separated. Empty means the
	// relay believes nobody, and reports a forwarded claim as a claim.
	TrustedProxies string // BEACON_TRUSTED_PROXIES
}

func loadConfig() Config {
	return Config{
		Listen:          env("BEACON_LISTEN", ":8080"),
		AdminToken:      envOrFile("BEACON_ADMIN_TOKEN", ""),
		StateDir:        env("BEACON_STATE_DIR", "/pki"),
		VaultAddr:       env("BEACON_VAULT_ADDR", ""),
		VaultTokenFile:  env("BEACON_VAULT_TOKEN_FILE", ""),
		VaultToken:      env("BEACON_VAULT_TOKEN", ""),
		VaultRoleID:     env("BEACON_VAULT_ROLE_ID", ""),
		VaultSecretID:   env("BEACON_VAULT_SECRET_ID", ""),
		VaultTransitKey: env("BEACON_VAULT_TRANSIT_KEY", "beacon-agent-assertions"),
		TrustedProxies:  env("BEACON_TRUSTED_PROXIES", ""),
	}
}

// healthURL turns the listen address into something the healthcheck subcommand
// can dial from inside the same container.
func (c Config) healthURL() string {
	addr := c.Listen
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	return "http://" + addr + "/healthz"
}

// envOrFile prefers the contents of the file named by <key>_FILE over the value
// of <key> itself.
//
// A value in the environment is visible to `docker inspect`, to anything that
// dumps the process environment, and to every child process. A file is not, and
// it is what a container runtime's own secret support mounts. The plain
// variable stays supported because a development stack has no reason to bother.
func envOrFile(key, def string) string {
	path := strings.TrimSpace(os.Getenv(key + "_FILE"))
	if path == "" {
		return env(key, def)
	}
	v, err := readSecretFile(path)
	if err != nil {
		// Fatal rather than a silent fall back to the plain variable or the
		// default: starting open because a secret file was unreadable is
		// exactly how a relay ends up unauthenticated without anyone meaning
		// it to.
		slog.Error("cannot use secret file, refusing to fall back",
			"var", key+"_FILE", "path", path, "err", err)
		os.Exit(1)
	}
	return v
}

func readSecretFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(data))
	if v == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return v, nil
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
