package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/bolchisb/go-beacon/internal/protocol"
)

const (
	// A debugging session wants the tail of a build log, not a core dump. Both
	// caps exist so one runaway command cannot exhaust the agent's memory.
	maxOutputBytes   = 1 << 20 // 1 MiB per stream, stdout and stderr each
	maxReadFileBytes = 4 << 20 // 4 MiB
	defaultTimeout   = 60 * time.Second
	maxTimeout       = 10 * time.Minute
)

// handleRPC answers exactly one request and lets the stream close. Errors are
// reported inside the response rather than by hanging up, so the caller always
// learns why.
func handleRPC(stream net.Conn, br *bufio.Reader) {
	var req protocol.RPCRequest
	if err := json.NewDecoder(br).Decode(&req); err != nil {
		slog.Warn("rpc: unreadable request", "err", err)
		writeRPC(stream, protocol.RPCResponse{Error: "unreadable request: " + err.Error()})
		return
	}

	slog.Info("rpc", "op", req.Op, "path", req.Path)
	writeRPC(stream, dispatchRPC(req))
}

func writeRPC(w net.Conn, resp protocol.RPCResponse) {
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Warn("rpc: cannot write response", "err", err)
	}
}

func dispatchRPC(req protocol.RPCRequest) protocol.RPCResponse {
	switch req.Op {
	case protocol.OpExec:
		return opExec(req)
	case protocol.OpReadFile:
		return opReadFile(req)
	case protocol.OpWriteFile:
		return opWriteFile(req)
	case protocol.OpListDir:
		return opListDir(req)
	case protocol.OpClipboardRead:
		return opClipboardRead()
	case protocol.OpClipboardWrite:
		return opClipboardWrite(req)
	case protocol.OpClipboardWriteImage:
		return opClipboardWriteImage(req)
	case protocol.OpClipboardReadImage:
		return opClipboardReadImage()
	default:
		return protocol.RPCResponse{Error: fmt.Sprintf("unknown operation %q", req.Op)}
	}
}

func opExec(req protocol.RPCRequest) protocol.RPCResponse {
	if req.Command == "" {
		return protocol.RPCResponse{Error: "command is required"}
	}

	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if timeout > maxTimeout {
		timeout = maxTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := shellCommand(ctx, req.Command, req.Dir)
	var stdout, stderr capWriter
	stdout.limit, stderr.limit = maxOutputBytes, maxOutputBytes
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()

	resp := protocol.RPCResponse{
		Stdout:    string(stdout.buf),
		Stderr:    string(stderr.buf),
		Truncated: stdout.truncated || stderr.truncated,
	}
	switch {
	case err == nil:
		resp.ExitCode = cmd.ProcessState.ExitCode()
	case ctx.Err() != nil:
		// the exit code would be a signal artefact; say what really happened
		resp.Error = fmt.Sprintf("timed out after %s", timeout)
		resp.ExitCode = -1
	default:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// a non-zero exit is an outcome, not a transport failure
			resp.ExitCode = ee.ExitCode()
		} else {
			resp.Error = err.Error()
			resp.ExitCode = -1
		}
	}
	return resp
}

func opReadFile(req protocol.RPCRequest) protocol.RPCResponse {
	if req.Path == "" {
		return protocol.RPCResponse{Error: "path is required"}
	}
	info, err := os.Stat(req.Path)
	if err != nil {
		return protocol.RPCResponse{Error: err.Error()}
	}
	if info.IsDir() {
		return protocol.RPCResponse{Error: req.Path + " is a directory"}
	}
	if info.Size() > maxReadFileBytes {
		return protocol.RPCResponse{Error: fmt.Sprintf("file is %d bytes, limit is %d", info.Size(), maxReadFileBytes)}
	}
	data, err := os.ReadFile(req.Path)
	if err != nil {
		return protocol.RPCResponse{Error: err.Error()}
	}
	return protocol.RPCResponse{Content: base64.StdEncoding.EncodeToString(data)}
}

func opWriteFile(req protocol.RPCRequest) protocol.RPCResponse {
	if req.Path == "" {
		return protocol.RPCResponse{Error: "path is required"}
	}
	data, err := base64.StdEncoding.DecodeString(req.Content)
	if err != nil {
		return protocol.RPCResponse{Error: "content is not valid base64: " + err.Error()}
	}
	if err := os.WriteFile(req.Path, data, 0o644); err != nil {
		return protocol.RPCResponse{Error: err.Error()}
	}
	return protocol.RPCResponse{Written: len(data)}
}

func opListDir(req protocol.RPCRequest) protocol.RPCResponse {
	path := req.Path
	if path == "" {
		path = "."
	}
	items, err := os.ReadDir(path)
	if err != nil {
		return protocol.RPCResponse{Error: err.Error()}
	}
	entries := make([]protocol.DirEntry, 0, len(items))
	for _, it := range items {
		e := protocol.DirEntry{Name: it.Name(), IsDir: it.IsDir()}
		if info, err := it.Info(); err == nil {
			e.Size = info.Size()
			e.Mode = info.Mode().String()
			e.ModTime = info.ModTime()
		}
		entries = append(entries, e)
	}
	return protocol.RPCResponse{Entries: entries}
}

// capWriter keeps the first limit bytes and counts the rest as lost. Capping on
// the way in matters: a command that prints forever must not be able to grow
// the agent's heap while it does.
type capWriter struct {
	buf       []byte
	limit     int
	truncated bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.limit - len(w.buf); room > 0 {
		if len(p) <= room {
			w.buf = append(w.buf, p...)
		} else {
			w.buf = append(w.buf, p[:room]...)
			w.truncated = true
		}
	} else {
		w.truncated = true
	}
	// always report a full write: the command must not see a short-write error
	return len(p), nil
}
