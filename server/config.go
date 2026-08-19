package main

import (
	"os"
	"strings"
)

// Config is read from the environment so the same binary works under compose
// and under k3s without a config file.
type Config struct {
	Listen string // BEACON_LISTEN, e.g. ":8080"
}

func loadConfig() Config {
	return Config{
		Listen: env("BEACON_LISTEN", ":8080"),
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
