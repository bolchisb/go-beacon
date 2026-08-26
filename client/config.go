package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
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

	// Token authenticates this machine to the relay's API, which is what
	// `beacon ssh` and `beacon forward` go through. The agent tunnel does not
	// use it -- that path authenticates on its own terms -- so a target machine
	// has no reason to carry one and only a workstation needs it set.
	//
	// This is the relay's admin token, which is also its recovery credential.
	// Prefer `beacon login`: it trades the operator password for a session and
	// stores only that, so the password never lands on disk and what does is
	// short-lived.
	Token string `json:"token,omitempty"`

	// Username is remembered so `beacon login` only has to ask for a password.
	// It is not a secret.
	Username string `json:"username,omitempty"`

	// Session is what `beacon login` obtains: a signed cookie with its own
	// expiry, held instead of the password that produced it.
	Session string `json:"session,omitempty"`

	// AgentKey is this machine's private key, generated here at install and
	// never sent anywhere. Losing it means re-enrolling; leaking it means
	// someone can impersonate this agent and only this agent.
	AgentKey string `json:"agent_key,omitempty"`

	// Assertion is the relay's Vault-signed statement binding AgentKey's public
	// half to this agent's id, until it expires.
	Assertion string `json:"assertion,omitempty"`

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
	keyToken  = "token"
	keyUser   = "user"
)

// Session is deliberately absent: it is obtained by `beacon login` and expires
// on its own, so it is not something to set by hand or report as a setting.
var configKeys = []string{keyServer, keyID, keyCA, keyToken, keyUser}

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
		r.set(keyToken, fc.Token, fromFile)
		r.set(keyUser, fc.Username, fromFile)
		r.Session = fc.Session
		r.AgentKey = fc.AgentKey
		r.Assertion = fc.Assertion
	}

	r.set(keyServer, os.Getenv("BEACON_SERVER"), fromEnv)
	r.set(keyID, os.Getenv("BEACON_AGENT_ID"), fromEnv)
	r.set(keyCA, os.Getenv("BEACON_CA_FILE"), fromEnv)
	r.set(keyToken, os.Getenv("BEACON_TOKEN"), fromEnv)
	r.set(keyUser, os.Getenv("BEACON_USER"), fromEnv)

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
	case keyToken:
		r.Token = value
	case keyUser:
		r.Username = value
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
	case keyToken:
		return r.Token
	case keyUser:
		return r.Username
	}
	return ""
}

// apiHeader carries the operator credential on requests to the relay's API.
//
// A session from `beacon login` is preferred over the admin token: it is what
// the operator's own username and password produce, it expires on its own, and
// it leaves the relay's recovery credential on the relay. The token stays
// supported for scripts and for a relay that has no account yet.
//
// Nil when there is neither, which keeps the header absent rather than empty on
// a relay that has no gate at all.
func apiHeader(token, session string) http.Header {
	h := http.Header{}
	switch {
	case session != "":
		h.Add("Cookie", sessionCookieName+"="+session)
	case token != "":
		h.Set("Authorization", "Bearer "+token)
	default:
		return nil
	}
	return h
}

// sessionCookieName matches what the relay sets. Kept here rather than shared
// through internal/protocol: it is an HTTP detail of the dashboard, not part of
// the wire contract between an agent and the relay.
const sessionCookieName = "beacon_session"

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
	// 0600, not 0644: this file carries the agent's credentials, and it lives in
	// a system-wide directory that every local account can read.
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return "", err
	}
	// WriteFile only applies the mode when it creates the file, so a config
	// written by an older build keeps its 0644 until it is tightened here.
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	return path, nil
}
