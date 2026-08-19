# go-beacon

**Develop on your own laptop. Run in the customer's environment.**

go-beacon is a lightweight client/server relay written in Go. It lets a
development team reach a machine inside a customer's environment — a VM behind
NAT, a firewalled build host, a locked-down cloud subnet — without a VPN, an
inbound firewall rule, or a public IP on the target.

A small agent is installed on the target machine and dials **outbound** to the
relay. The relay holds that connection open and multiplexes everything over it:
a terminal, a command, a file transfer. Developers work against the customer's
real environment from their own workstation, and when something only reproduces
there, the same tunnel is the debugging channel.

## How it works

```
 developer workstation          relay (server)              customer VM
        │                            │                           │
        │  ── dashboard / terminal ─► │ ◄──── outbound tunnel ────│  agent
        │  ── AI assistant (MCP) ──► │        (multiplexed)       │
        │  ── local port (RDP) ────► │                            │
```

- **Outbound only.** The agent initiates the connection. Nothing is exposed on
  the customer's network.
- **One connection, one port.** A terminal, a command and a file transfer are
  each just a stream inside the connection the agent already opened. There is no
  second port to open, on either end.
- **Self-healing.** The agent supervises its own session and reconnects with
  exponential backoff and jitter; a relay restart needs nobody to log into the
  target machine.
- **Small and portable.** One static binary per platform, pure Go, no runtime
  dependencies. Windows, Linux and macOS on amd64 and arm64.
- **Operator dashboard.** A built-in web UI shows connected agents, transferred
  bytes and a live activity feed.

## Capabilities

| Capability | Description | Status |
| --- | --- | --- |
| Relay tunnel | Multiplexed, outbound-only agent sessions | Available |
| Dashboard & API | Agent inventory, connectivity test, live event stream | Available |
| Web terminal | Full interactive shell in a browser tab, one click from the dashboard | Available (Linux, macOS) |
| MCP bridge | Commands, files and clipboard on a target, driven by an AI assistant | Available |
| Port forwarding | A local port that leads to a service on the target: remote desktop, or anything else it hosts | Available |
| Clipboard | Read and replace the target's clipboard | Available (Windows, macOS) |
| Windows terminal | The same shell on Windows targets, over ConPTY | Planned |
| mTLS | Mutual TLS between agent and relay, internal CA | Deferred |

## Running the relay

The relay runs in a container; no Go toolchain is required on the host.

```sh
make up          # build and start the relay on :8080
make client      # cross-compile the agent into dist/ for every platform
make logs        # follow relay logs
make down        # stop the relay
```

The dashboard is then at <http://127.0.0.1:8080/ui/>. `BEACON_LISTEN` (default
`:8080`) sets the listen address.

For a deployment that runs a published image instead of building from source,
see [`docker-compose.prod.yml`](docker-compose.prod.yml) and
[`.env.example`](.env.example).

## Installing the agent

Copy the binary for the target's platform out of `dist/`, then:

```sh
beacon install --server http://relay.example.com:8080 --id build-vm-01
```

That registers it as a system service and starts it. Other commands:

| Command | Purpose |
| --- | --- |
| `beacon run` | Run in the foreground instead of as a service |
| `beacon status` | Show whether the agent is connected, and its round-trip time |
| `beacon config` | Show every setting and where its value came from |
| `beacon forward` | Open a local port that leads to a service on a remote machine |
| `beacon update` | Replace the binary with the latest release |
| `beacon start` / `stop` / `restart` | Control the installed service |
| `beacon uninstall` | Remove the service and the binary |

Settings resolve from defaults, then the config file, then the environment, then
flags — later wins.

| Flag | Environment variable | Default | Purpose |
| --- | --- | --- | --- |
| `--server` | `BEACON_SERVER` | `http://127.0.0.1:8080` | Relay URL |
| `--id` | `BEACON_AGENT_ID` | hostname | Identity shown in the dashboard |
| `--ca-file` | `BEACON_CA_FILE` | — | Extra trusted CA bundle (HTTPS only) |

One setting has no flag and no environment variable, because it belongs to the
machine rather than to a session: `services` in the config file lists what the
agent is willing to forward.

```json
{ "services": { "rdp": "127.0.0.1:3389" } }
```

Entries are merged over the defaults, and an empty address withdraws a service.
A stream names a service, never an address — otherwise every agent would be a
route into the network behind it.

## Working on a remote machine

**A terminal.** Press **SSH** next to an agent in the dashboard. A new tab opens
with a real terminal on that machine — a genuine pseudo-terminal, so full-screen
programs, colours and window resizing all behave.

**A remote desktop.** The relay answers on one port and nothing else, so the
port a desktop client needs is opened next to that client rather than on the
relay:

```sh
beacon forward build-vm-01 rdp --listen 127.0.0.1:3390
```

Point Remote Desktop at `127.0.0.1:3390`. Each connection to that port becomes
its own session through the tunnel; the relay reads none of it. The same works
for any service the agent lists under `services`.

**An AI assistant.** Point it at the relay's MCP endpoint:

```sh
claude mcp add --transport http beacon http://relay.example.com:8080/mcp
```

It gains seven tools — `list_agents`, `run_command`, `read_file`, `write_file`,
`list_dir`, `read_clipboard` and `write_clipboard` — each naming the agent it
should act on. `list_agents` first; everything else needs an id from it.

The clipboard tools need a desktop session on the target. Windows and macOS
always have one; a headless Linux host has no clipboard at all, and says so
rather than failing obscurely.

## Security

The relay is currently unauthenticated, and both the terminal and the MCP
endpoint grant command execution on every connected machine. Run it on a trusted
network only: bind it to loopback or a private interface, as
`docker-compose.prod.yml` does by default, and do not publish it on a routable
address.

Mutual TLS and per-developer authorization are the next milestone. Agent
identity moves from the connection headers to the client certificate at that
point, and nothing else in the relay has to change for it.

## License

go-beacon is **source-available and dual-licensed**. It is free for private
individuals in personal, non-commercial use. Any use by an organization of any
kind requires a paid commercial license. See [LICENSE.md](LICENSE.md) for the
full terms.

Copyright © 2026 Bogdan Bolchis.
