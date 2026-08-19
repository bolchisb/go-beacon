# go-beacon

**Develop on your own laptop. Run in the customer's environment.**

go-beacon is a lightweight client/server relay written in Go. It lets a
development team reach a machine inside a customer's environment — a VM behind
NAT, a firewalled build host, a locked-down cloud subnet — without a VPN, an
inbound firewall rule, or a public IP on the target.

A small agent is installed on the target machine and dials **outbound** to the
relay. The relay keeps that connection open and multiplexes every session over
it, so developers work against the customer's real environment from their own
workstation, with their own editor and their own tooling. The same tunnel
doubles as a debugging channel into a running system when something only
reproduces there.

## How it works

```
 developer workstation          relay (server)              customer VM
        │                            │                           │
        │  ─── dashboard / API ────► │ ◄──── outbound tunnel ─────│  agent
        │                            │        (multiplexed)       │
```

- **Outbound only.** The agent initiates the connection. Nothing has to be
  exposed on the customer's network.
- **One connection, many sessions.** All traffic is multiplexed over a single
  persistent stream, so a shell, a file copy and a debug session share one
  socket.
- **Self-healing.** The agent supervises its own session and reconnects with
  exponential backoff and jitter; a relay restart does not require anyone to
  log into the target machine.
- **Small and portable.** A single static binary per platform, pure Go, no
  runtime dependencies. Windows, Linux and macOS on amd64 and arm64.
- **Operator dashboard.** The relay serves a built-in web UI showing connected
  agents, transferred bytes and a live activity feed.

## Capabilities

| Capability | Description | Status |
| --- | --- | --- |
| Relay tunnel | Multiplexed, outbound-only agent sessions | Available |
| Dashboard & API | Agent inventory, connectivity test, live event stream | Available |
| mTLS | Mutual TLS between agent and relay, internal CA | In progress |
| SSH | Interactive shell and port forwarding to the target machine | Planned |
| SCP / SFTP | File transfer in both directions | Planned |
| RDP | Remote desktop forwarding for Windows targets | Planned |
| MCP | Model Context Protocol bridge for AI-assisted workflows | Planned |

## Quick start

The relay runs in a container; no Go toolchain is required on the host.

```sh
make up          # build and start the relay on :8080
make client      # cross-compile the agent into dist/ for all platforms
make logs        # follow relay logs
make down        # stop the relay
```

Open the dashboard at <http://127.0.0.1:8080/ui/>.

Then run the agent on the target machine:

```sh
./beacon-agent -server http://127.0.0.1:8080 -id build-vm-01
```

| Flag | Environment variable | Default | Purpose |
| --- | --- | --- | --- |
| `-server` | `BEACON_SERVER` | `http://127.0.0.1:8080` | Relay URL |
| `-id` | `BEACON_AGENT_ID` | hostname | Identity shown in the dashboard |
| `-ca-file` | `BEACON_CA_FILE` | — | Extra trusted CA bundle (HTTPS only) |

The relay reads `BEACON_LISTEN` (default `:8080`) for its listen address.

## Security

The relay currently terminates plain HTTP and the dashboard is unauthenticated;
it is intended to run on a trusted network or behind a reverse proxy until
mutual TLS lands. Agent identity moves from the connection headers to the
client certificate at that point.

## License

go-beacon is **source-available and dual-licensed**. It is free for private
individuals in personal, non-commercial use. Any use by an organization of any
kind requires a paid commercial license. See [LICENSE.md](LICENSE.md) for the
full terms.

Copyright © 2026 Bogdan Bolchis.
