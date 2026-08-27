package main

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/bolchisb/go-beacon/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type agentArgs struct {
	Agent string `json:"agent" jsonschema:"agent id, as reported by list_agents"`
}

type clipboardArgs struct {
	Agent string `json:"agent" jsonschema:"agent id, as reported by list_agents"`
	Text  string `json:"text" jsonschema:"text to place on the machine's clipboard"`
}

func (s *Server) toolReadClipboard(_ context.Context, _ *mcp.CallToolRequest, in agentArgs) (*mcp.CallToolResult, any, error) {
	resp, err := s.agentRPC(in.Agent, protocol.RPCRequest{Op: protocol.OpClipboardRead})
	if err != nil {
		return errorResult("%v", err), nil, nil
	}
	if resp.Error != "" {
		return errorResult("%s", resp.Error), nil, nil
	}

	text, err := base64.StdEncoding.DecodeString(resp.Content)
	if err != nil {
		return errorResult("agent returned unreadable content: %v", err), nil, nil
	}
	if len(text) == 0 {
		return textResult("The clipboard is empty."), nil, nil
	}
	if resp.Truncated {
		return textResult(string(text) + "\n--- clipboard was truncated at the 1 MiB cap ---"), nil, nil
	}
	return textResult(string(text)), nil, nil
}

func (s *Server) toolWriteClipboard(_ context.Context, _ *mcp.CallToolRequest, in clipboardArgs) (*mcp.CallToolResult, any, error) {
	resp, err := s.agentRPC(in.Agent, protocol.RPCRequest{
		Op:      protocol.OpClipboardWrite,
		Content: base64.StdEncoding.EncodeToString([]byte(in.Text)),
	})
	if err != nil {
		return errorResult("%v", err), nil, nil
	}
	if resp.Error != "" {
		return errorResult("%s", resp.Error), nil, nil
	}
	return textResult(fmt.Sprintf("copied %d bytes to the clipboard of %s", resp.Written, in.Agent)), nil, nil
}
