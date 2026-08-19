package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Rollback is the only thing between a bad release and a fleet that cannot be
// reached, so the previous build has to be kept on every platform, not just the
// one that forces it.
func TestReplaceBinaryKeepsThePreviousBuild(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "beacon")
	writeFile(t, exe, "old build")

	if err := replaceBinary(exe, []byte("new build")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, exe); got != "new build" {
		t.Fatalf("binary is %q, want the new build", got)
	}
	if got := read(t, exe+".old"); got != "old build" {
		t.Fatalf("previous build is %q, want the old one", got)
	}
	if _, err := os.Stat(exe + ".new"); err == nil {
		t.Fatal("the staging file should be gone")
	}
}

func TestReplaceBinaryWorksWhenThereIsNothingToReplace(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "beacon")

	if err := replaceBinary(exe, []byte("first build")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, exe); got != "first build" {
		t.Fatalf("binary is %q", got)
	}
}

func TestRollbackRestoresAndKeepsTheEvidence(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "beacon")
	writeFile(t, exe, "old build")
	if err := replaceBinary(exe, []byte("bad build")); err != nil {
		t.Fatal(err)
	}

	// serviceStop is expected to fail here; rollback must carry on regardless
	if err := rollback(exe); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := read(t, exe); got != "old build" {
		t.Fatalf("binary is %q, want the old build back", got)
	}
	if got := read(t, exe+".failed"); got != "bad build" {
		t.Fatalf("the bad build should be kept as evidence, got %q", got)
	}
}

func TestRollbackRefusesWhenThereIsNothingToGoBackTo(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "beacon")
	writeFile(t, exe, "only build")

	if err := rollback(exe); err == nil {
		t.Fatal("expected an error when no previous build exists")
	}
	if got := read(t, exe); got != "only build" {
		t.Fatalf("the binary must be left alone, got %q", got)
	}
}

// A fleet arriving at GitHub in the same second after a release is the thing
// jitter exists to prevent.
func TestJitterStaysWithinAQuarterOfTheInterval(t *testing.T) {
	const base = time.Hour
	seenLow, seenHigh := false, false

	for i := 0; i < 500; i++ {
		d := jitterAround(base)
		if d < base*3/4 || d > base*5/4 {
			t.Fatalf("jitterAround(%s) = %s, outside 75-125%%", base, d)
		}
		if d < base {
			seenLow = true
		}
		if d > base {
			seenHigh = true
		}
	}
	if !seenLow || !seenHigh {
		t.Fatal("jitter should spread on both sides of the interval")
	}
}
