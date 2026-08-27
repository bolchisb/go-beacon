package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

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

type clipboardImageArgs struct {
	Agent string `json:"agent" jsonschema:"agent id, as reported by list_agents"`
	Image string `json:"image" jsonschema:"a PNG image, base64 encoded, at most 8 MiB"`
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

// toolReadClipboardImage hands the assistant a screenshot the operator copied
// on the target machine.
//
// This is the direction that pays: somebody working on a machine sees something
// wrong, presses print screen, and asks about it. Without this the assistant is
// told about a picture it cannot look at, on a machine it has no other way to
// see.
func (s *Server) toolReadClipboardImage(_ context.Context, _ *mcp.CallToolRequest, in agentArgs) (*mcp.CallToolResult, any, error) {
	resp, err := s.agentRPC(in.Agent, protocol.RPCRequest{Op: protocol.OpClipboardReadImage})
	if err != nil {
		return errorResult("%v", err), nil, nil
	}
	if resp.Error == protocol.ClipboardNoImage {
		// Not an error result: the assistant asked a reasonable question and
		// this is the answer, so it should read it and move on rather than
		// treat the call as having failed.
		return textResult("There is no image on that machine's clipboard. " +
			"It may hold text instead — read_clipboard answers that."), nil, nil
	}
	if resp.Error != "" {
		return errorResult("%s", resp.Error), nil, nil
	}

	raw, err := base64.StdEncoding.DecodeString(resp.Content)
	if err != nil {
		return errorResult("agent returned unreadable image data: %v", err), nil, nil
	}
	if resp.Truncated {
		// A truncated PNG is not an image, it is a prefix. Saying so beats
		// handing over something that will not decode.
		return errorResult("that image is over the 8 MiB limit and would arrive incomplete"), nil, nil
	}

	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: fmt.Sprintf("The clipboard of %s holds this image (%d bytes):", in.Agent, len(raw))},
		&mcp.ImageContent{Data: raw, MIMEType: "image/png"},
	}}, nil, nil
}

func (s *Server) toolWriteClipboardImage(_ context.Context, _ *mcp.CallToolRequest, in clipboardImageArgs) (*mcp.CallToolResult, any, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(in.Image))
	if err != nil {
		return errorResult("image must be base64: %v", err), nil, nil
	}
	if len(raw) > protocol.MaxClipboardImageBytes {
		return errorResult("that image is %d bytes, over the 8 MiB limit", len(raw)), nil, nil
	}

	resp, err := s.agentRPC(in.Agent, protocol.RPCRequest{
		Op:      protocol.OpClipboardWriteImage,
		Content: base64.StdEncoding.EncodeToString(raw),
	})
	if err != nil {
		return errorResult("%v", err), nil, nil
	}
	if resp.Error != "" {
		return errorResult("%s", resp.Error), nil, nil
	}
	return textResult(fmt.Sprintf("placed a %d byte image on the clipboard of %s", resp.Written, in.Agent)), nil, nil
}
