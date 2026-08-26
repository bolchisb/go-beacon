package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIHeaderCarriesWhicheverCredentialExists(t *testing.T) {
	// A relay with no gate should see no credential header at all, rather than
	// an empty one it then has to decide what to do with.
	if h := apiHeader("", ""); h != nil {
		t.Errorf("got %v, want no header", h)
	}

	if got := apiHeader("s3cret", "").Get("Authorization"); got != "Bearer s3cret" {
		t.Errorf("got %q, want a bearer header", got)
	}

	if got := apiHeader("", "sess").Get("Cookie"); got != sessionCookieName+"=sess" {
		t.Errorf("got %q, want a session cookie", got)
	}

	// A session comes from the operator's own password and expires on its own,
	// so it should win over the relay's admin token when both are present.
	both := apiHeader("s3cret", "sess")
	if both.Get("Cookie") == "" {
		t.Error("the session was not sent")
	}
	if both.Get("Authorization") != "" {
		t.Error("the admin token was sent alongside a session")
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

func TestLoginReturnsTheSessionCookie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.FormValue("username") != "alice" || r.FormValue("password") != "hunter2" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "the-session"})
		// The relay redirects a browser after a successful sign-in; the client
		// must read the response that carries the cookie rather than follow it.
		w.Header().Set("Location", "/ui/")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	got, err := login(srv.URL, "", "alice", "hunter2")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if got != "the-session" {
		t.Errorf("session is %q, want the cookie value", got)
	}

	if _, err := login(srv.URL, "", "alice", "wrong"); err == nil {
		t.Error("bad credentials were accepted")
	}
}

func TestLoginSaysSoWhenThereIsNoAccountYet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := login(srv.URL, "", "alice", "hunter2")
	if err == nil || !strings.Contains(err.Error(), "no operator account") {
		t.Errorf("got %v, want a message pointing at the dashboard", err)
	}
}
