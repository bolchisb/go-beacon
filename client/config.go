package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Config is the persisted settings file. It deliberately holds only what a
// service cannot be told on a command line.
type Config struct {
	Server  string `json:"server"`
	AgentID string `json:"agent_id"`
	CAFile  string `json:"ca_file,omitempty"`

	// AutoUpdate is a pointer so that an absent field means enabled: an agent
	// on a machine nobody can reach must not be left behind by a config written
	// before the setting existed.
	AutoUpdate *bool `json:"auto_update,omitempty"`

	// Services maps a forwardable service name to the local address the agent
	// dials for it. Absent entries fall back to the built-in defaults; an empty
	// address withdraws a service. Edited in the file, not in the form: this is
	// a per-machine detail, not something you change while logging in.
	Services map[string]string `json:"services,omitempty"`
}

// source records where an effective value came from. Reporting this is most of
// the value of `beacon config`: "why is it dialling the wrong relay" should
// take one command to answer.
type source string

const (
	fromDefault source = "default"
	fromFile    source = "config file"
	fromEnv     source = "env"
	fromFlag    source = "flag"
)

const (
	keyServer = "server"
	keyID     = "id"
	keyCA     = "ca-file"
)

var configKeys = []string{keyServer, keyID, keyCA}

type resolved struct {
	Config
	sources map[string]source
	path    string
	exists  bool
}

func defaultConfig() Config {
	host, _ := os.Hostname()
	return Config{Server: "http://127.0.0.1:8080", AgentID: host}
}

// loadConfig applies defaults, then the config file, then the environment,
// then explicitly passed flags. Later wins.
func loadConfig(flags map[string]string) (*resolved, error) {
	r := &resolved{
		Config:  defaultConfig(),
		sources: map[string]source{},
		path:    configPath(),
	}
	for _, k := range configKeys {
		r.sources[k] = fromDefault
	}

	fc, exists, err := readConfigFile(r.path)
	if err != nil {
		return nil, err
	}
	r.exists = exists
	if exists {
		// Services has no flag and no environment variable, so it is carried
		// straight across rather than going through set()
		r.Services = fc.Services
		r.AutoUpdate = fc.AutoUpdate
		r.set(keyServer, fc.Server, fromFile)
		r.set(keyID, fc.AgentID, fromFile)
		r.set(keyCA, fc.CAFile, fromFile)
	}

	r.set(keyServer, os.Getenv("BEACON_SERVER"), fromEnv)
	r.set(keyID, os.Getenv("BEACON_AGENT_ID"), fromEnv)
	r.set(keyCA, os.Getenv("BEACON_CA_FILE"), fromEnv)

	for k, v := range flags {
		r.set(k, v, fromFlag)
	}
	return r, nil
}

func (r *resolved) set(key, value string, src source) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	switch key {
	case keyServer:
		r.Server = value
	case keyID:
		r.AgentID = value
	case keyCA:
		r.CAFile = value
	default:
		return
	}
	r.sources[key] = src
}

// autoUpdate reports whether the agent should update itself.
func (r *resolved) autoUpdate() bool {
	return r.AutoUpdate == nil || *r.AutoUpdate
}

func (r *resolved) value(key string) string {
	switch key {
	case keyServer:
		return r.Server
	case keyID:
		return r.AgentID
	case keyCA:
		return r.CAFile
	}
	return ""
}

func readConfigFile(path string) (Config, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, false, fmt.Errorf("%s: %w", path, err)
	}
	return c, true, nil
}

func saveConfig(c Config) (string, error) {
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
