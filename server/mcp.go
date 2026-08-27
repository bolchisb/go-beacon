package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/bolchisb/go-beacon/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newMCPServer exposes the relay's capabilities as MCP tools, so an assistant
// on a developer's laptop can drive a machine it has no route to. Every tool
// takes an agent id, because "which machine" is never implicit here.
func newMCPServer(s *Server) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:        "go-beacon",
		Title:       "go-beacon relay",
		Description: "Run commands and inspect files on machines connected to this relay.",
		Version:     version,
	}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_agents",
		Description: "List the machines connected to the relay. Call this first: every other tool needs an agent id from here.",
	}, s.toolListAgents)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "run_command",
		Description: "Run a shell command on a connected machine and return its output and exit code. " +
			"Runs through /bin/sh on unix and cmd on windows. Output is capped at 1 MiB per stream.",
	}, s.toolRunCommand)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "read_clipboard_image",
		Description: "Look at an image on a connected machine's clipboard — a screenshot somebody " +
			"copied there. Answers in words if the clipboard holds no image. PNG, up to 8 MiB.",
	}, s.toolReadClipboardImage)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "write_clipboard_image",
		Description: "Place a PNG image, base64 encoded, on a connected machine's clipboard, " +
			"so a program running there can paste it. Up to 8 MiB.",
	}, s.toolWriteClipboardImage)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "read_file",
		Description: "Read a file from a connected machine. Files larger than 4 MiB are refused.",
	}, s.toolReadFile)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "write_file",
		Description: "Write a file on a connected machine, replacing it if it exists.",
	}, s.toolWriteFile)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "read_clipboard",
		Description: "Read the clipboard of a connected machine. " +
			"Only works where there is a desktop session; a headless linux host has no clipboard.",
	}, s.toolReadClipboard)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "write_clipboard",
		Description: "Replace the clipboard contents of a connected machine.",
	}, s.toolWriteClipboard)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_dir",
		Description: "List the contents of a directory on a connected machine.",
	}, s.toolListDir)

	return srv
}

type execArgs struct {
	Agent          string `json:"agent" jsonschema:"agent id, as reported by list_agents"`
	Command        string `json:"command" jsonschema:"shell command to run"`
	Dir            string `json:"dir,omitempty" jsonschema:"working directory, defaults to the agent's own"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"how long to wait, default 60, maximum 600"`
}

type pathArgs struct {
	Agent string `json:"agent" jsonschema:"agent id, as reported by list_agents"`
	Path  string `json:"path" jsonschema:"absolute path on the target machine"`
}

type writeArgs struct {
	Agent   string `json:"agent" jsonschema:"agent id, as reported by list_agents"`
	Path    string `json:"path" jsonschema:"absolute path on the target machine"`
	Content string `json:"content" jsonschema:"the file's new contents, as plain text"`
}

func (s *Server) toolListAgents(_ context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
	agents := s.registry.Snapshot()
	if len(agents) == 0 {
		return textResult("No agents have ever connected to this relay."), nil, nil
	}

	var b strings.Builder
	for _, a := range agents {
		state := "offline"
		if a.Online {
			state = "online"
		}
		fmt.Fprintf(&b, "%s\t%s\t%s/%s\t%s", a.ID, state, a.OS, a.Arch, a.Hostname)
		if a.RTTms != nil {
			fmt.Fprintf(&b, "\trtt %.1fms", *a.RTTms)
		}
		b.WriteString("\n")
	}
	return textResult(b.String()), nil, nil
}

func (s *Server) toolRunCommand(_ context.Context, _ *mcp.CallToolRequest, in execArgs) (*mcp.CallToolResult, any, error) {
	if in.Command == "" {
		return errorResult("command is required"), nil, nil
	}
	resp, err := s.agentRPC(in.Agent, protocol.RPCRequest{
		Op:             protocol.OpExec,
		Command:        in.Command,
		Dir:            in.Dir,
		TimeoutSeconds: in.TimeoutSeconds,
	})
	if err != nil {
		return errorResult("%v", err), nil, nil
	}
	if resp.Error != "" {
		return errorResult("%s", resp.Error), nil, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "exit code: %d\n", resp.ExitCode)
	if resp.Stdout != "" {
		b.WriteString("--- stdout ---\n" + resp.Stdout)
		if !strings.HasSuffix(resp.Stdout, "\n") {
			b.WriteString("\n")
		}
	}
	if resp.Stderr != "" {
		b.WriteString("--- stderr ---\n" + resp.Stderr)
		if !strings.HasSuffix(resp.Stderr, "\n") {
			b.WriteString("\n")
		}
	}
	if resp.Stdout == "" && resp.Stderr == "" {
		b.WriteString("(no output)\n")
	}
	if resp.Truncated {
		b.WriteString("--- output was truncated at the 1 MiB cap ---\n")
	}
	return textResult(b.String()), nil, nil
}

func (s *Server) toolReadFile(_ context.Context, _ *mcp.CallToolRequest, in pathArgs) (*mcp.CallToolResult, any, error) {
	resp, err := s.agentRPC(in.Agent, protocol.RPCRequest{Op: protocol.OpReadFile, Path: in.Path})
	if err != nil {
		return errorResult("%v", err), nil, nil
	}
	if resp.Error != "" {
		return errorResult("%s", resp.Error), nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(resp.Content)
	if err != nil {
		return errorResult("agent returned unreadable content: %v", err), nil, nil
	}
	// Handing an assistant a wall of base64 helps nobody, so binary files say
	// so rather than pretending to be text.
	if !utf8.Valid(data) {
		return textResult(fmt.Sprintf("%s is binary (%d bytes); not shown as text.", in.Path, len(data))), nil, nil
	}
	return textResult(string(data)), nil, nil
}

func (s *Server) toolWriteFile(_ context.Context, _ *mcp.CallToolRequest, in writeArgs) (*mcp.CallToolResult, any, error) {
	resp, err := s.agentRPC(in.Agent, protocol.RPCRequest{
		Op:      protocol.OpWriteFile,
		Path:    in.Path,
		Content: base64.StdEncoding.EncodeToString([]byte(in.Content)),
	})
	if err != nil {
		return errorResult("%v", err), nil, nil
	}
	if resp.Error != "" {
		return errorResult("%s", resp.Error), nil, nil
	}
	return textResult(fmt.Sprintf("wrote %d bytes to %s", resp.Written, in.Path)), nil, nil
}

func (s *Server) toolListDir(_ context.Context, _ *mcp.CallToolRequest, in pathArgs) (*mcp.CallToolResult, any, error) {
	resp, err := s.agentRPC(in.Agent, protocol.RPCRequest{Op: protocol.OpListDir, Path: in.Path})
	if err != nil {
		return errorResult("%v", err), nil, nil
	}
	if resp.Error != "" {
		return errorResult("%s", resp.Error), nil, nil
	}
	if len(resp.Entries) == 0 {
		return textResult(in.Path + " is empty"), nil, nil
	}

	var b strings.Builder
	for _, e := range resp.Entries {
		kind := "file"
		if e.IsDir {
			kind = "dir"
		}
		fmt.Fprintf(&b, "%s\t%s\t%d\t%s\n", kind, e.Mode, e.Size, e.Name)
	}
	return textResult(b.String()), nil, nil
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// errorResult reports a failure through the result rather than through a
// protocol error, which is what lets the assistant read it and try again.
func errorResult(format string, a ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, a...)}},
	}
}
