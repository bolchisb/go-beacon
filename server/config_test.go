package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSecretFile(t *testing.T) {
	dir := t.TempDir()

	// Trailing newlines are what every `echo secret > file` leaves behind, so
	// they must not become part of the token.
	good := filepath.Join(dir, "token")
	if err := os.WriteFile(good, []byte("  s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readSecretFile(good)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3cret" {
		t.Errorf("got %q, want the trimmed contents", got)
	}

	if _, err := readSecretFile(filepath.Join(dir, "absent")); err == nil {
		t.Error("a missing file was accepted")
	}

	blank := filepath.Join(dir, "blank")
	if err := os.WriteFile(blank, []byte("\n\t "), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecretFile(blank); err == nil {
		t.Error("a whitespace-only file was accepted -- that would start the relay open")
	}
}

func TestEnvOrFilePrefersTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEACON_TEST_SECRET", "from-env")
	t.Setenv("BEACON_TEST_SECRET_FILE", path)

	if got := envOrFile("BEACON_TEST_SECRET", "fallback"); got != "from-file" {
		t.Errorf("got %q, want the file to win over the environment", got)
	}
}

func TestEnvOrFileFallsBackWhenNoFileIsNamed(t *testing.T) {
	t.Setenv("BEACON_TEST_SECRET", "from-env")
	if got := envOrFile("BEACON_TEST_SECRET", "fallback"); got != "from-env" {
		t.Errorf("got %q, want the environment value", got)
	}
	os.Unsetenv("BEACON_TEST_SECRET")
	if got := envOrFile("BEACON_TEST_SECRET", "fallback"); got != "fallback" {
		t.Errorf("got %q, want the default", got)
	}
}
