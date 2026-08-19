package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/bolchisb/go-beacon/internal/protocol"
)

// errAgentOffline lets callers answer 404 rather than guessing at a status
// from an error string.
var errAgentOffline = errors.New("agent is not connected")

// rpcTimeout only stops a wedged stream from pinning a goroutine: the agent
// already bounds how long a command may run.
const rpcTimeout = 11 * time.Minute

// openAgentStream opens one stream to a connected agent and declares what it
// carries. Every capability the relay offers passes through here.
func (s *Server) openAgentStream(agentID, kind string) (net.Conn, error) {
	sess, ok := s.registry.Session(agentID)
	if !ok {
		return nil, fmt.Errorf("%q: %w", agentID, errAgentOffline)
	}
	stream, err := sess.OpenStream()
	if err != nil {
		return nil, err
	}
	if err := protocol.WriteStreamHeader(stream, kind); err != nil {
		stream.Close()
		return nil, err
	}
	return stream, nil
}

// agentRPC performs one request/response call against an agent. The stream is
// used once and closed, which is what keeps the wire contract free of request
// ids: yamux already gives every call its own channel.
func (s *Server) agentRPC(agentID string, req protocol.RPCRequest) (protocol.RPCResponse, error) {
	stream, err := s.openAgentStream(agentID, protocol.StreamRPC)
	if err != nil {
		return protocol.RPCResponse{}, err
	}
	defer stream.Close()

	if err := stream.SetDeadline(time.Now().Add(rpcTimeout)); err != nil {
		return protocol.RPCResponse{}, err
	}
	if err := json.NewEncoder(stream).Encode(req); err != nil {
		return protocol.RPCResponse{}, err
	}

	var resp protocol.RPCResponse
	if err := json.NewDecoder(bufio.NewReader(stream)).Decode(&resp); err != nil {
		return protocol.RPCResponse{}, err
	}
	return resp, nil
}
