package main

import (
	"os"
	"strings"
)

// Config is read from the environment so the same binary works under compose
// and under k3s without a config file.
type Config struct {
	Listen string // BEACON_LISTEN, e.g. ":8080"

	// AdminToken gates the dashboard, the API and the MCP endpoint.
	// BEACON_ADMIN_TOKEN; empty leaves them open, which is announced at startup.
	AdminToken string

	// StateDir is the one writable path the relay has. It holds no secrets --
	// only the cached transit public keys, which is what lets agent
	// verification survive a Vault outage.
	StateDir string // BEACON_STATE_DIR

	// Vault issues and signs agent credentials. Either VaultToken or the
	// AppRole pair authenticates the relay to it; compose uses a token in
	// development and AppRole in production.
	VaultAddr       string // BEACON_VAULT_ADDR
	VaultToken      string // BEACON_VAULT_TOKEN
	VaultRoleID     string // BEACON_VAULT_ROLE_ID
	VaultSecretID   string // BEACON_VAULT_SECRET_ID
	VaultTransitKey string // BEACON_VAULT_TRANSIT_KEY
}

func loadConfig() Config {
	return Config{
		Listen:          env("BEACON_LISTEN", ":8080"),
		AdminToken:      env("BEACON_ADMIN_TOKEN", ""),
		StateDir:        env("BEACON_STATE_DIR", "/pki"),
		VaultAddr:       env("BEACON_VAULT_ADDR", ""),
		VaultToken:      env("BEACON_VAULT_TOKEN", ""),
		VaultRoleID:     env("BEACON_VAULT_ROLE_ID", ""),
		VaultSecretID:   env("BEACON_VAULT_SECRET_ID", ""),
		VaultTransitKey: env("BEACON_VAULT_TRANSIT_KEY", "beacon-agent-assertions"),
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

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
