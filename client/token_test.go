package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAPIHeaderIsAbsentWithoutAToken(t *testing.T) {
	// A relay with no gate should see no Authorization header at all, rather
	// than an empty one it then has to decide what to do with.
	if h := apiHeader(""); h != nil {
		t.Errorf("got %v, want no header", h)
	}
	h := apiHeader("s3cret")
	if got := h.Get("Authorization"); got != "Bearer s3cret" {
		t.Errorf("got %q, want a bearer header", got)
	}
}

func TestTokenResolvesFromFileEnvAndFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"server":"http://x","token":"from-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEACON_CONFIG", path)

	r, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Token != "from-file" {
		t.Errorf("token is %q, want the config file value", r.Token)
	}

	t.Setenv("BEACON_TOKEN", "from-env")
	if r, _ = loadConfig(nil); r.Token != "from-env" {
		t.Errorf("token is %q, want the environment to win over the file", r.Token)
	}

	if r, _ = loadConfig(map[string]string{keyToken: "from-flag"}); r.Token != "from-flag" {
		t.Errorf("token is %q, want the flag to win over everything", r.Token)
	}
}

func TestTokenIsReportedByConfigShow(t *testing.T) {
	// `beacon config` exists to answer "where did this value come from", so a
	// token that resolves but is not listed would defeat the point.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"server":"http://x","token":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEACON_CONFIG", path)

	r, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, k := range configKeys {
		if k == keyToken {
			found = true
		}
	}
	if !found {
		t.Fatal("token is not among the reported config keys")
	}
	if r.value(keyToken) != "t" {
		t.Errorf("value(token) is %q, want t", r.value(keyToken))
	}
}
