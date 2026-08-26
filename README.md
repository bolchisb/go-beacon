# go-beacon

[![server image](https://github.com/bolchisb/go-beacon/actions/workflows/server-image.yml/badge.svg)](https://github.com/bolchisb/go-beacon/actions/workflows/server-image.yml)
[![Go 1.26](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License: source-available](https://img.shields.io/badge/license-source--available-orange)](LICENSE.md)
[![Platforms: Windows, Linux, macOS](https://img.shields.io/badge/platforms-windows%20%7C%20linux%20%7C%20macos-lightgrey)](#requirements)

**Develop on your own laptop. Run in the customer's environment.**

go-beacon is a client/server relay written in Go. It gives a development team a
terminal, a file transfer, a forwarded port or an AI assistant on a machine
inside a customer's environment — a VM behind NAT, a firewalled build host, a
locked-down cloud subnet — without a VPN, an inbound firewall rule, or a public
IP on the target.

A small agent is installed on the target and dials **outbound** to the relay. The
relay holds that connection open and multiplexes everything over it. Developers
work against the customer's real environment from their own workstation, and when
a defect only reproduces there, the same tunnel is the debugging channel.

> [!WARNING]
> **The relay is currently unauthenticated.** Anyone who can reach it has command
> execution on every connected machine. Bind it to loopback or a private
> interface and do not publish it on a routable address. See
> [Security](#security) before deploying.

> [!IMPORTANT]
> go-beacon is **source-available, not open source**. It is free for private
> individuals in personal, non-commercial use. Any use by an organization of any
> kind requires a paid commercial license. See [Licensing](#licensing).

## Contents

- [At a glance](#at-a-glance)
- [How it works](#how-it-works)
- [Capabilities](#capabilities)
- [Security](#security)
- [Requirements](#requirements)
- [Running the relay](#running-the-relay)
- [Roles: target machine and workstation](#roles-target-machine-and-workstation)
- [Installing on a target machine](#installing-on-a-target-machine)
- [Setting up your workstation](#setting-up-your-workstation)
- [Usage](#usage)
- [Roadmap](#roadmap)
- [Licensing](#licensing)

## At a glance

| | |
| --- | --- |
| Toolchain | Go 1.26, pure Go, `CGO_ENABLED=0` |
| Artifacts | One static binary per platform; the relay also ships as a container image |
| Platforms | Windows, Linux and macOS on amd64 and arm64 |
| Network requirement | A single outbound TCP connection from the target to the relay |
| Ports opened on the target | None |
| Transport security | TLS when the relay is served over HTTPS; mutual TLS is [planned](#roadmap) |
| Authentication | **None yet** — see [Security](#security) |
| Runtime dependencies | None |
| License | Source-available, dual-licensed |

## How it works

```
developer workstation                relay                 target machine
          │                            │                          │
          │  ─ dashboard / terminal ─► │                          │
          │  ─ AI assistant (MCP) ───► │ ◄─── outbound tunnel ────│  agent
          │  ─ local port (ssh/rdp) ─► │       (multiplexed)      │
          │                            │                          │
                                                       no inbound port is opened
```

- **Outbound only.** The agent initiates the connection. Nothing is exposed on
  the customer's network.
- **One connection, one port.** A terminal, a command and a file transfer are
  each a stream inside the connection the agent already opened. There is no
  second port to open, on either end.
- **Self-healing.** The agent supervises its own session and reconnects with
  exponential backoff and jitter. A relay restart requires nobody to log into the
  target machine.
- **Small and portable.** One static binary per platform, pure Go, no runtime
  dependencies.
- **Operator dashboard.** A built-in web UI shows connected agents, transferred
  bytes and a live activity feed.

## Capabilities

| Capability | Description | Status |
| --- | --- | --- |
| Relay tunnel | Multiplexed, outbound-only agent sessions | Available |
| Dashboard & API | Agent inventory, connectivity test, live event stream | Available |
| Web terminal | Full interactive shell in a browser tab, one click from the dashboard | Available |
| MCP bridge | Commands, files and clipboard on a target, driven by an AI assistant | Available |
| Port forwarding | A local port that leads to a service on the target: ssh, remote desktop, or anything else it hosts | Available |
| Clipboard | Read and replace the target's clipboard | Available (Windows, macOS) |
| Mutual TLS | Mutual TLS between agent and relay, internal CA | Planned |
| Per-developer authorization | Named operators, scoped to specific agents | Planned |

## Security

Understand the current posture before deploying go-beacon anywhere.

**What is protected.** The agent connects outbound and no port is opened on the
target. A forwarded stream names a service, never an address, so an agent cannot
be used as a general route into the network behind it. Traffic is encrypted
whenever the relay is served over HTTPS, and `--ca-file` supplies a private CA
bundle for a relay behind an internal certificate authority.

**What is not protected yet.** The relay does not authenticate its callers. Both
the web terminal and the MCP endpoint grant command execution on every connected
machine, to anyone who can reach the relay. Treat network reachability as the
only access control that currently exists.

**Deployment guidance.**

- Bind the relay to loopback or a private interface, as
  [`docker-compose.prod.yml`](docker-compose.prod.yml) does by default.
- Do not publish the relay on a routable address.
- Place it behind an authenticating reverse proxy or a VPN if it must be reached
  across a network boundary.

Mutual TLS and per-developer authorization are the next milestone. Agent identity
moves from the connection headers to the client certificate at that point, and
nothing else in the relay has to change for it.

## Requirements

| | |
| --- | --- |
| Relay host | Docker and Docker Compose. No Go toolchain required. |
| Target machine | None. A single static binary, plus administrative rights to install the service. |
| Workstation | None for the browser terminal and the MCP endpoint. The binary is needed only for `beacon ssh` and `beacon forward`. |
| Building from source | Go 1.26 or later |

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

To run a published image instead of building from source, see
[`docker-compose.prod.yml`](docker-compose.prod.yml) and
[`.env.example`](.env.example).

## Roles: target machine and workstation

The same binary serves two roles, and confusing them is the most common mistake.

| | Target machine | Your own machine |
| --- | --- | --- |
| What you run | `beacon install`, once | `beacon forward`, when you need it |
| How it runs | A system service, as root or LocalSystem | An ordinary foreground process |
| Needs elevation | Yes | No |
| Appears in the dashboard | Yes | No |

> [!CAUTION]
> **Do not run `beacon install` on your own laptop.** It would register the
> laptop as a target: it would appear in the dashboard, and anyone who can reach
> the relay would have a shell on it.

## Installing on a target machine

Copy the binary for the target's platform out of `dist/`, then:

```sh
beacon install --server http://relay.example.com:8080 --id build-vm-01
```

That registers it as a system service and starts it.

### Agent commands

| Command | Purpose |
| --- | --- |
| `beacon run` | Run in the foreground instead of as a service |
| `beacon status` | Show whether the agent is connected, and its round-trip time |
| `beacon config` | Show every setting and where its value came from |
| `beacon ssh` | Open a terminal on a machine, in this terminal |
| `beacon forward` | Open a local port that leads to a service on a remote machine |
| `beacon update` | Replace the binary with the latest release, verify it, roll back if it fails |
| `beacon start` / `stop` / `restart` | Control the installed service |
| `beacon uninstall` | Remove the service and the binary |
| `beacon version` | Print the build version |

### Automatic updates

The installed agent checks for a new release every hour and updates itself. The
check is jittered so a fleet does not arrive at GitHub together, it is skipped
while anyone has a session open through that agent, and the restart is handed to
a separate process which restores the previous binary if the new one does not
come up.

Set `"auto_update": false` in the config file to turn it off.

> [!NOTE]
> Only the installed service updates itself, never a copy you are running by
> hand.

### Logging

An agent's warnings and errors are sent to the relay and appear in the
dashboard's event feed, which is usually where you want them: a machine you
cannot log into is exactly the one whose log you cannot read. Everything below
warning stays local — the relay already records sessions opening and closing.

The full local log remains available:

| Platform | Location |
| --- | --- |
| Linux | `journalctl -u beacon` |
| macOS | `/var/log/beacon.log` |
| Windows | `%ProgramData%\beacon\beacon.log` |

Windows is the case that matters most, since a service there has no stdout to
inherit at all.

### Configuration

Settings resolve from defaults, then the config file, then the environment, then
flags — later wins.

| Flag | Environment variable | Default | Purpose |
| --- | --- | --- | --- |
| `--server` | `BEACON_SERVER` | `http://127.0.0.1:8080` | Relay URL |
| `--id` | `BEACON_AGENT_ID` | Hostname | Identity shown in the dashboard |
| `--ca-file` | `BEACON_CA_FILE` | — | Extra trusted CA bundle (HTTPS only) |

One setting has no flag and no environment variable, because it belongs to the
machine rather than to a session: `services` in the config file lists what the
agent is willing to forward.

```json
{ "services": { "rdp": "127.0.0.1:3389", "ssh": "127.0.0.1:22" } }
```

Those two are the defaults, so an agent forwards a desktop and a shell without
being told to. Entries are merged over the defaults, and an empty address
withdraws a service.

> [!NOTE]
> A stream names a service, never an address. Otherwise every agent would be a
> route into the network behind it.

## Setting up your workstation

Nothing to install. You need the binary only for `beacon ssh` and
`beacon forward` — the browser terminal and the MCP endpoint need nothing local
at all.

```sh
mkdir -p ~/bin
wget -qO ~/bin/beacon https://github.com/bolchisb/go-beacon/releases/latest/download/beacon-darwin-arm64
chmod +x ~/bin/beacon
```

Substitute the file name for your platform: `beacon-linux-amd64`,
`beacon-windows-amd64.exe`, and so on. Each release also publishes a
`SHA256SUMS` file for verification.

> [!TIP]
> Fetching the binary with `wget` or `curl` also avoids the quarantine flag macOS
> applies to anything downloaded through a browser.

`beacon forward` needs no root: it listens on a high port and exits on Ctrl-C.
Point it at the relay with `--server`, or save that once with
`beacon config set server=https://relay.example.com`.

## Usage

Every route below requires the agent to show as **connected** in the dashboard.

### Choosing a route

| You want to | Use | Needs the binary locally |
| --- | --- | --- |
| Look at something quickly | The browser terminal | No |
| A shell in your own terminal | `beacon ssh` | Yes |
| `scp`, `rsync`, ssh config | `beacon forward … ssh` | Yes |
| A desktop | `beacon forward … rdp` | Yes |
| Let an assistant do it | The MCP endpoint | No |

### Browser terminal

Press **SSH** next to an agent in the dashboard. A new tab opens with a real
terminal on that machine — a genuine pseudo-terminal, so full-screen programs,
colours and window resizing all behave.

### A shell in your own terminal

```sh
beacon ssh build-vm-01
```

This carries the same stream the dashboard uses and lands in a shell the same
way, with no password in between: the relay is the authority here, not the
target's account database.

> [!NOTE]
> That is also its limit. `beacon ssh` is not the ssh protocol, so it cannot
> carry `scp` or `rsync`. For those, forward the target's own sshd.

### A real ssh client

`scp`, `rsync`, `~/.ssh/config` and agent forwarding all need the real client,
which means the target's own sshd and its own credentials. Open the port in one
terminal and leave it running:

```sh
beacon forward build-vm-01 ssh --listen 127.0.0.1:2222
```

Then work against it from anywhere else, as usual:

```sh
ssh -p 2222 you@127.0.0.1
scp -P 2222 ./patch.diff you@127.0.0.1:/tmp/
rsync -e 'ssh -p 2222' -av ./src/ you@127.0.0.1:/opt/app/
```

The target's own sshd authenticates, with the accounts and keys that machine
already has, so this route **does** ask for a password unless your key is already
in its `authorized_keys`. Nothing about your ssh client changes: it talks to a
local port, and the bytes leave over 443.

> [!WARNING]
> Note the lower-case `-p`. `ssh -P` is silently ignored and connects to port 22
> on your own machine instead, which looks like the tunnel asking for a password
> when it is really your laptop.

For something you use daily, name it once in `~/.ssh/config`:

```
Host build-vm-01
    HostName 127.0.0.1
    Port 2222
    User you
```

### Remote desktop

The relay answers on one port and nothing else, so the port a desktop client
needs is opened next to that client rather than on the relay:

```sh
beacon forward build-vm-01 rdp --listen 127.0.0.1:3390
```

Point Remote Desktop at `127.0.0.1:3390`. Each connection to that port becomes
its own session through the tunnel; the relay reads none of it.

> [!NOTE]
> Remote Desktop has to be switched on in the target's own Windows settings.
> go-beacon does not enable it, and if it is off the agent logs a refused
> connection on port 3389.

### AI assistant (MCP)

Point the assistant at the relay's MCP endpoint:

```sh
claude mcp add --transport http beacon http://relay.example.com:8080/mcp
```

It gains seven tools, each naming the agent it should act on:

| Tool | Purpose |
| --- | --- |
| `list_agents` | List the machines connected to the relay |
| `run_command` | Run a shell command and return its output and exit code |
| `read_file` | Read a file from a connected machine |
| `write_file` | Write a file, replacing it if it exists |
| `list_dir` | List the contents of a directory |
| `read_clipboard` | Read the target's clipboard |
| `write_clipboard` | Replace the target's clipboard |

Call `list_agents` first; every other tool needs an id from it. From there it is
ordinary conversation:

> on build-vm-01, run the test suite and show me what failed

The assistant runs the command on that machine, reads the files it needs and
reports back, without anyone opening a terminal.

> [!CAUTION]
> The MCP endpoint grants command execution on every connected machine, with no
> authentication. See [Security](#security).

### Clipboard

Two of those tools move text between machines, which is the quickest way to carry
a stack trace or a connection string across:

> read the clipboard on build-vm-01

> [!NOTE]
> Windows and macOS always have a clipboard. A headless Linux host has none, and
> says so rather than failing obscurely.

## Roadmap

| Milestone | Scope |
| --- | --- |
| Mutual TLS | Mutual TLS between agent and relay, backed by an internal CA. Agent identity moves from the connection headers to the client certificate. |
| Per-developer authorization | Named operators, scoped to specific agents. |

## Licensing

go-beacon is **source-available and dual-licensed**. It is *not* open source as
defined by the Open Source Initiative.

| Who you are | Terms |
| --- | --- |
| A private individual, personal and non-commercial use | Free of charge |
| An organization of any kind | Paid commercial license required |

See [LICENSE.md](LICENSE.md) for the full terms. For commercial licensing,
contact **Bogdan Bolchis** — <bolchisb@gmail.com>.

Copyright © 2026 Bogdan Bolchis. All rights reserved.
