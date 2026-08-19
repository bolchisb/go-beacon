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
| Port forwarding | A local port that leads to a service on the target: ssh, remote desktop, or anything else it hosts | Available |
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

## Two machines, two jobs

The same binary plays two parts, and confusing them is the easiest mistake to
make.

| | The target machine | Your own machine |
| --- | --- | --- |
| What you run | `beacon install`, once | `beacon forward`, when you need it |
| How it runs | a system service, as root or LocalSystem | an ordinary foreground process |
| Needs elevation | yes | no |
| Shows in the dashboard | yes | no |

**Do not run `beacon install` on your own laptop.** It would register the laptop
as a target: it would appear in the dashboard, and anyone who can reach the
relay would have a shell on it.

## On the target machine

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
{ "services": { "rdp": "127.0.0.1:3389", "ssh": "127.0.0.1:22" } }
```

Those two are the defaults, so an agent forwards a desktop and a shell without
being told to.

Entries are merged over the defaults, and an empty address withdraws a service.
A stream names a service, never an address — otherwise every agent would be a
route into the network behind it.

## On your own machine

Nothing to install. You only need the binary, and only for `beacon forward` —
the browser terminal and the MCP endpoint need nothing local at all.

```sh
mkdir -p ~/bin
wget -qO ~/bin/beacon https://github.com/bolchisb/go-beacon/releases/latest/download/beacon-darwin-arm64
chmod +x ~/bin/beacon
```

Swap the file name for your platform: `beacon-linux-amd64`,
`beacon-windows-amd64.exe`, and so on. Fetching it with `wget` or `curl` also
avoids the quarantine flag macOS puts on anything downloaded through a browser.

`beacon forward` needs no root: it listens on a high port and exits when you
press Ctrl-C. Point it at the relay with `--server`, or save that once with
`beacon config set server=https://relay.example.com`.

## Working on a remote machine

Every route below needs the agent shown as **connected** in the dashboard.
Start here if you are not sure which one you want:

| You want to | Use | Needs the binary locally |
| --- | --- | --- |
| Look at something quickly | the browser terminal | no |
| Actually work: `scp`, `rsync`, ssh config | `beacon forward … ssh` | yes |
| A desktop | `beacon forward … rdp` | yes |
| Let an assistant do it | the MCP endpoint | no |

**A terminal.** Press **SSH** next to an agent in the dashboard. A new tab opens
with a real terminal on that machine — a genuine pseudo-terminal, so full-screen
programs, colours and window resizing all behave.

**A real ssh client.** The browser terminal is the quick way in, but `scp`,
`rsync`, `~/.ssh/config` and agent forwarding all need the real client. Open the
port in one terminal and leave it running:

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
already has. Nothing about your ssh client changes: it talks to a local port,
and the bytes leave over 443.

For something you use daily, name it once in `~/.ssh/config`:

```
Host build-vm-01
    HostName 127.0.0.1
    Port 2222
    User you
```

**A remote desktop.** The relay answers on one port and nothing else, so the
port a desktop client needs is opened next to that client rather than on the
relay:

```sh
beacon forward build-vm-01 rdp --listen 127.0.0.1:3390
```

Point Remote Desktop at `127.0.0.1:3390`. Each connection to that port becomes
its own session through the tunnel; the relay reads none of it.

Remote Desktop has to be switched on in the target's own Windows settings.
Beacon does not enable it, and if it is off the agent logs a refused connection
on port 3389.

**An AI assistant.** Point it at the relay's MCP endpoint:

```sh
claude mcp add --transport http beacon http://relay.example.com:8080/mcp
```

It gains seven tools — `list_agents`, `run_command`, `read_file`, `write_file`,
`list_dir`, `read_clipboard` and `write_clipboard` — each naming the agent it
should act on. `list_agents` first; the rest need an id from it. From there it
is ordinary conversation:

> on build-vm-01, run the test suite and show me what failed

The assistant runs the command on that machine, reads the files it needs and
reports back, without anyone opening a terminal.

**The clipboard.** Two of those tools move text between machines, which is the
quickest way to carry a stack trace or a connection string across:

> read the clipboard on build-vm-01

Windows and macOS always have a clipboard. A headless Linux host has none, and
says so rather than failing obscurely.

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
