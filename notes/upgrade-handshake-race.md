# Race on the HTTP upgrade handshake

**Date:** 2026-08-19 · **Status:** fixed and verified

## Symptom

The agent failed to establish a tunnel roughly once every eight connection
attempts, with:

```
level=WARN msg="session ended" err="server sent data before the upgrade completed"
```

Retries succeeded, so the failure looked transient and harmless. It was not.

## Reproduction

Start the relay, run the agent, and force repeated reconnects. The failure
needs the server and agent to be close enough that the server's first frame
overtakes the client's parse of the handshake response — over loopback it hit
within a dozen attempts.

## Root cause

`serveSession` starts `pingLoop`, which sends a yamux ping **immediately**,
microseconds after the server writes `101 Switching Protocols`. When both land
in the same TCP segment, the client's `bufio.Reader` — created to parse the
HTTP response — reads the ping bytes into its buffer along with the headers.

Those bytes belong to the tunnel. `yamux.Client` is handed the bare `net.Conn`,
never sees them, and the stream is silently short by a frame.

The client had a defensive check that turned this into an error. The detection
was right; the treatment was wrong. Discarding the bytes and retrying is not a
fix, it is a coin flip.

## Fix

`protocol.WithBuffered(conn, br)` wraps the connection so it yields whatever the
reader already buffered before falling through to the socket:

```go
io.MultiReader(io.LimitReader(br, int64(br.Buffered())), conn)
```

Applied on both sides. The client needs it because the server pings on connect;
the server needs it because a pipelining agent or an intermediate proxy can put
the agent's first frames behind the request.

## Verification

150 rapid connect/kill cycles against the relay: 149 tunnels established (the
last process was killed before it flushed its log), **zero** handshake errors.

## What this rules out for later

An L7 proxy in front of the relay makes segment coalescing *more* likely, not
less — nginx buffers the upstream response before switching to tunnel mode. Any
future transport change that reintroduces a bare `net.Conn` after an HTTP parse
reopens this. The wrapper is the invariant, not the workaround.
