package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	xssh "golang.org/x/crypto/ssh"
)

// dialEmbedded runs the agent's ssh server on one end of a connection and
// returns a client on the other. It still goes through HandleConn, which is
// the production path: the agent hands it a single already-authenticated
// stream and never listens on a port.
//
// A loopback pair rather than net.Pipe. net.Pipe is unbuffered and synchronous,
// and both ends of an ssh handshake write their version banner before reading
// one, so the two writes deadlock against each other.
func dialEmbedded(t *testing.T) *xssh.Client {
	t.Helper()
	resetSSHServer()

	dir := t.TempDir()
	t.Setenv("BEACON_CONFIG", filepath.Join(dir, "config.json"))
	cfg := &Config{Server: "http://127.0.0.1:8080", AgentID: "test"}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		handleSSH(conn, cfg)
	}()

	client, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	conn, chans, reqs, err := xssh.NewClientConn(client, "agent", &xssh.ClientConfig{
		User:            "operator",
		Auth:            []xssh.AuthMethod{xssh.Password("anything")},
		HostKeyCallback: xssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	})
	if err != nil {
		t.Fatalf("ssh handshake failed: %v", err)
	}
	c := xssh.NewClient(conn, chans, reqs)
	t.Cleanup(func() { c.Close() })
	return c
}

func TestEmbeddedSSHRunsACommand(t *testing.T) {
	c := dialEmbedded(t)
	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	out, err := sess.Output("echo hello-from-the-agent")
	if err != nil {
		t.Fatalf("running a command failed: %v", err)
	}
	if !strings.Contains(string(out), "hello-from-the-agent") {
		t.Errorf("got %q, want the command's output", out)
	}
}

func TestEmbeddedSSHReportsTheRealExitCode(t *testing.T) {
	// scp, rsync and git all branch on the exit status. A blanket failure would
	// make a successful transfer look broken and the reverse.
	c := dialEmbedded(t)
	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	err = sess.Run("exit 42")
	var ee *xssh.ExitError
	if !asExitError(err, &ee) {
		t.Fatalf("got %v, want an exit error", err)
	}
	if ee.ExitStatus() != 42 {
		t.Errorf("exit status is %d, want 42", ee.ExitStatus())
	}
}

func TestEmbeddedSSHServesSFTP(t *testing.T) {
	// This is what VS Code Remote-SSH and scp need; without it an editor
	// connects, reports success, and cannot open a single file.
	c := dialEmbedded(t)
	fs, err := sftp.NewClient(c)
	if err != nil {
		t.Fatalf("sftp subsystem unavailable: %v", err)
	}
	defer fs.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "written-over-sftp.txt")

	f, err := fs.Create(path)
	if err != nil {
		t.Fatalf("creating a file over sftp failed: %v", err)
	}
	if _, err := f.Write([]byte("round trip")); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the file did not land on disk: %v", err)
	}
	if string(got) != "round trip" {
		t.Errorf("file holds %q, want what was written", got)
	}
}

func TestHostKeyIsStableAcrossConnections(t *testing.T) {
	// A key that changed per connection would make every ssh client refuse the
	// second one with a host-key warning.
	resetSSHServer()
	dir := t.TempDir()
	t.Setenv("BEACON_CONFIG", filepath.Join(dir, "config.json"))

	cfg := &Config{Server: "http://x", AgentID: "test"}
	first, err := hostKey(cfg)
	if err != nil {
		t.Fatal(err)
	}
	stored := cfg.SSHHostKey
	if stored == "" {
		t.Fatal("no host key was stored")
	}

	second, err := hostKey(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.PublicKey().Marshal()) != string(second.PublicKey().Marshal()) {
		t.Error("the host key changed between calls")
	}
}

func asExitError(err error, target **xssh.ExitError) bool {
	e, ok := err.(*xssh.ExitError)
	if ok {
		*target = e
	}
	return ok
}
