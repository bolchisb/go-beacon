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

> [!IMPORTANT]
> Nothing reaches this relay unauthenticated. Operators sign in with a username
> and password; agents prove themselves with a key generated on their own
> machine and a statement the relay's Vault signed for it. Read
> [Security](#security) for what that does and does not cover.

> [!IMPORTANT]
> go-beacon is **source-available, not open source**. It is free for private
> individuals in personal, non-commercial use. Any use by an organization of any
> kind requires a paid commercial license. See [Licensing](#licensing).

## Contents

- [At a glance](#at-a-glance)
- [How it works](#how-it-works)
- [Capabilities](#capabilities)
- [How this was built](#how-this-was-built)
- [Security](#security)
- [Requirements](#requirements)
- [Running the relay](#running-the-relay)
  - [Operator authentication](#operator-authentication)
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
| Transport security | TLS, terminated by the relay or by a proxy in front of it |
| Operator authentication | Username and password, stored in Vault, set up on first run |
| Agent authentication | Per-agent key, generated on the machine, bound by a Vault-signed assertion |
| Runtime dependencies | None on the agent. The relay needs a Vault, shipped alongside it |
| Development | AI-assisted — see [How this was built](#how-this-was-built) |
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
| Operator authentication | Username and password gating the dashboard, the API and the MCP endpoint | Available |
| Credential backend | Vault, unsealed automatically, issuing signed agent credentials | Available |
| Agent enrolment | Authorised by operator credentials at install; the key is generated on the machine | Available |
| Agent authentication | Vault-signed assertion plus a signed challenge on every connect | Available |
| Per-operator identity | Named operators rather than one shared token | Planned |

## How this was built

go-beacon is vibe-coded. It was designed and written with an AI assistant in the
loop for most of it, and that is stated here for the same reason the security
posture is: you should know what you are adopting.

**Why that is worth something.** The capability surface in the table above —
a cross-platform agent, native service integration on three operating systems, a
self-updating fleet, a browser terminal, a port forwarder and an MCP bridge —
is far more ground than a project this size would normally cover. Working this
way is what made that scope reachable, and it is what keeps the turnaround on a
reported bug measured in days rather than quarters. A fix costs about what a
feature costs here.

**What keeps it honest.** Generated code is not shipped on trust:

- The mechanisms most likely to bite are the ones under test: the operator gate
  and its session handling, credential loading, signature verification and key
  rotation, update and rollback, process supervision, port forwarding, the ssh
  command path, clipboard handling and config parsing. `make test` runs them;
  `make vet` gates `go vet` and `gofmt` over the whole tree.
- Hard failures get investigated to a root cause and written up rather than
  patched until the symptom disappears — an upgrade handshake race, the Windows
  auto-update path, the supervision loop. Those write-ups are kept internally.
- Every release is built reproducibly from a tagged commit, refuses to build
  from a dirty tree, and publishes `SHA256SUMS`.

**What to expect.** Bugs. Particularly away from the paths that get daily use:
unusual platforms, uncommon service configurations, the edges of the forwarding
and update logic. This is young software and it has not been through a security
audit or a formal review.

> [!TIP]
> Rough edges are the most useful thing you can send back. Open an issue with
> the platform, the command and what the dashboard's event feed showed —
> that is usually enough to reproduce it.

## Security

Understand the current posture before deploying go-beacon anywhere.

### What is protected

**Operator access.** The dashboard, the API and the MCP endpoint all grant
command execution on every connected machine, so all three sit behind one gate.
It is closed by default in the sense that matters: the exemption list names the
agent tunnel and the health probe explicitly, and anything added later is
protected until somebody deliberately opens it.

A browser exchanges the token for a session cookie. There is no session store,
because the relay has none: the cookie carries its own expiry and an HMAC over
it, keyed by the operator token. A restart therefore signs nobody out, and
rotating the token invalidates every outstanding session at once — which is also
the only revocation there is until per-operator identity lands.

**Credentials at rest.** The operator token is read from a mounted file rather
than an environment variable, so it does not appear in `docker inspect`, in the
process environment, or in anything that dumps either. The relay holds no Vault
credential at all: Vault Agent exchanges a response-wrapped, single-use secret id
for a short-lived token and keeps it renewed, and the relay only ever reads that
file.

**The agent's side of the tunnel.** The agent connects outbound and no port is
opened on the target. A forwarded stream names a service, never an address, so an
agent cannot be used as a general route into the network behind it.

**Agent identity.** Each agent generates a keypair on its own machine and the
private half never leaves it. Enrolment is authorised by an operator's username
and password, typed once by whoever is installing, used for that one request and
never stored on the target. What the machine keeps is a Vault-signed assertion
binding its id to its key — an identity for itself and nothing that reaches any
other machine.

On every connection the agent signs a challenge the relay has just issued.
Challenges are single use, so a captured handshake cannot be replayed, and the
id comes from the assertion rather than from a header, so an enrolled machine
cannot claim to be a different one. The relay verifies both without asking
Vault, which is why a sealed Vault stops enrolment without disconnecting anyone.

**The blast radius of a Vault outage.** The relay caches Vault's public keys, so
a sealed or unreachable Vault stops enrolment and renewal but does not disconnect
a fleet that is already running. That matters because the fleet is exactly the
set of machines nobody can reach to fix.

### What is not protected yet

**One shared operator token.** There is no per-operator identity, no attribution
of who did what, and no way to revoke one person without rotating for everyone.

**No audit trail for operator actions.** Vault records credential issuance; the
relay does not yet record who ran what.

**Nothing here has been through a security audit.**

### Deployment guidance

- Set an operator token. Without one the relay starts, says so loudly in its
  log, and serves all three surfaces to anyone who reaches it.
- Keep the relay on a private interface, or behind an authenticating proxy, for
  as long as agents remain unauthenticated.
- Vault must never be reachable by agents. It is not published in the shipped
  compose files, and it should stay that way: agents run on customer networks,
  and exposing a Vault to them is a worse exposure than the relay itself.

## Requirements

| | |
| --- | --- |
| Relay host | Docker and Docker Compose. No Go toolchain required. A Vault runs alongside the relay and is started by the same compose file. |
| Target machine | None. A single static binary, plus administrative rights to install the service. |
| Workstation | None for the browser terminal and the MCP endpoint. The binary is needed only for `beacon ssh` and `beacon forward`. |
| Building from source | Go 1.26 or later |

## Running the relay

The relay runs in containers alongside a Vault; no Go toolchain is required on
the host.

```sh
make up          # build and start the relay, Vault, and the two Vault helpers
make vault-init  # once, after the first start: create the signing key and role
make client      # cross-compile the agent into dist/ for every platform
make logs        # follow relay logs
make down        # stop everything
```

`make vault-init` is idempotent and only has to be run once per deployment.
Everything after it is automatic: Vault is initialised and unsealed on first
start and re-opened after every restart, and the relay's own credential is
minted, delivered wrapped, and renewed without anyone touching it.

The dashboard is then at <http://127.0.0.1:8080/ui/>. `BEACON_LISTEN` (default
`:8080`) sets the listen address.

Four containers make up the stack:

| Container | Job |
| --- | --- |
| `beacon-server` | The relay itself |
| `beacon-vault` | Stores the signing key and issues agent credentials |
| `beacon-vault-unseal` | Watches Vault and unseals it, at boot and any time it re-seals |
| `beacon-vault-agent` | Turns a wrapped, single-use secret id into a renewed token for the relay |

To run published images instead of building from source, see
[`docker-compose.prod.yml`](docker-compose.prod.yml) and
[`.env.example`](.env.example).

### Operator authentication

The dashboard, the API and the MCP endpoint all grant command execution on every
connected machine. One account gates all three.

**First, the admin token.** It authorises the initial setup and is the way back
in if the password is ever lost. Create it as a file on the relay host, outside
the repository so it can never be picked up by an image build:

```sh
umask 077 && openssl rand -base64 32 > /tmp/beacon-token
sudo install -D -m 0400 -o 65532 -g 65532 /tmp/beacon-token /etc/beacon/admin-token
shred -u /tmp/beacon-token
```

> [!IMPORTANT]
> The owner matters. The relay container runs as uid 65532 and Compose
> bind-mounts the file exactly as it finds it — `uid`, `gid` and `mode` under a
> service's `secrets:` entry are Swarm-only and are silently ignored otherwise.
> A root-owned `0600` file is unreadable to the relay, and the relay then
> **refuses to start** rather than starting without a gate. That is the intended
> failure, but it is a confusing one if the ownership is unexpected.

**Then, the operator account.** On a fresh deployment the dashboard shows a setup
form rather than a login: paste the admin token, choose a username and a
password, and that becomes the everyday credential. Setup closes behind itself — a second attempt is refused — so there
is no window in which whoever reaches the page first owns the relay, and no
generated password is ever written to a log.

The account lives in Vault, so it survives redeploys, and it is cached beside
the transit keys so that a sealed Vault does not lock you out of your own
dashboard. Change the password from the **Account** section of the dashboard;
doing so signs every other session out and leaves yours alone.

> [!WARNING]
> With no admin token and no account, the relay starts **open**. It logs a
> warning saying so and reports `auth_enabled: false` from `/api/server`, but it
> will serve the dashboard, the API and the MCP endpoint to anyone who reaches
> it.

Signing in depends on what is asking:

| Client | How |
| --- | --- |
| Browser | Username and password, exchanged for a session cookie lasting 12 hours |
| Browser, password lost | The admin token, under **Lost the password?** on the sign-in page |
| `curl`, scripts | `Authorization: Bearer <admin token>` |
| An AI assistant | The same header, passed when the MCP endpoint is registered |

The agent tunnel and the health probe are deliberately exempt: agents will
authenticate on their own terms, and the container health check runs with no
credentials to offer.

### How the relay gets its own credentials

Worth knowing if you operate this, and skippable otherwise.

Vault holds an ed25519 signing key that never leaves it. The relay is allowed to
ask for signatures and to read the public half — nothing else, and never the
private key.

It authenticates with none of that in its environment. The unseal supervisor,
which already holds the root token and already runs at every boot, mints a
**response-wrapped, single-use** secret id. Vault Agent unwraps it, logs in,
writes a short-lived token to a file and keeps it renewed. The relay reads only
that file, and picks up a rotated token on its next request.

The wrapping matters: a wrapped token can be unwrapped exactly once. If anyone
intercepts one in transit, the legitimate unwrap fails and Vault Agent refuses to
start, so an interception is something you find out about rather than something
you do not.

Unsealing is scripted rather than delegated to a cloud KMS, which means the key
shares live on the relay host, in a volume of their own, next to the data they
open. That is a deliberate trade for keeping the deployment self-contained, and
it means the host is part of the trust boundary.

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
beacon install --server https://relay.example.com --id target-01
```

It asks for an operator username and password, enrols the machine, then
registers it as a system service and starts it.

The credentials are used for that one request and are never written to the
machine. What stays behind is a keypair generated locally and the relay's
signed statement that it belongs to this agent id — so a compromised target
yields its own identity and nothing that reaches any other machine.

`beacon enroll` does the same thing on a machine that is already installed —
after an assertion expires, or when pointing an agent at a different relay —
without touching the service.

> [!NOTE]
> Enrolment needs the relay reachable and its Vault unsealed. An agent that is
> already enrolled keeps connecting through a Vault outage; only enrolling a new
> one has to wait.

> [!IMPORTANT]
> `beacon login` is a different thing and does not enrol anything. It signs an
> **operator** in, so that `beacon ssh` and `beacon forward` work from that
> machine. An agent needs `beacon enroll`.

### Agent commands

| Command | Purpose |
| --- | --- |
| `beacon run` | Run in the foreground instead of as a service |
| `beacon status` | Show whether the agent is connected, and its round-trip time |
| `beacon config` | Show every setting and where its value came from |
| `beacon enroll` | Give this machine an identity with the relay, without reinstalling the service |
| `beacon login` / `logout` | Sign in to the relay with the dashboard's username and password |
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
| `--token` | `BEACON_TOKEN` | — | The relay's admin token, for scripts; a person should use `beacon login` instead |
| `--user` | `BEACON_USER` | — | Operator username, remembered so `beacon login` only asks for a password |

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

Both `beacon ssh` and `beacon forward` go through the relay's API, so they need
you signed in where the relay has a gate. Use the same username and password as
the dashboard:

```sh
beacon login
```

The password is read once and never stored. What is kept is the session the
relay issues for it, which expires on its own — so a workstation config that
goes astray goes stale, and the relay's admin token stays on the relay. `beacon
logout` forgets it locally; changing the password in the dashboard is what
invalidates it everywhere.

The agent tunnel is not affected. An installed agent authenticates on its own
terms, so a target machine never needs any of this.

> [!NOTE]
> `--token` and `BEACON_TOKEN` still accept the relay's admin token, for scripts
> and for a relay with no account yet. Prefer `beacon login` for a person: the
> admin token is also the recovery credential, and copying it onto every machine
> that wants a shell means it can no longer be revoked for one of them.

Given a relay that wants credentials and a client with none, both commands say
so rather than reporting a bare refusal.

## Usage

Every route below requires the agent to show as **connected** in the dashboard.

### Choosing a route

| You want to | Use | Needs the binary locally |
| --- | --- | --- |
| Look at something quickly | The browser terminal | No |
| A shell in your own terminal | `beacon ssh` | Yes |
| `scp`, `rsync`, ssh config | `beacon forward … ssh` | Yes |
| Write code in their environment | VS Code over `beacon forward … ssh --stdio` | Yes |
| A desktop | `beacon forward … rdp` | Yes |
| Let an assistant do it | The MCP endpoint | No |

### Browser terminal

Press **SSH** next to an agent in the dashboard. A new tab opens with a real
terminal on that machine — a genuine pseudo-terminal, so full-screen programs,
colours and window resizing all behave.

### A shell in your own terminal

```sh
beacon ssh target-01
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
beacon forward target-01 ssh --listen 127.0.0.1:2222
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

For something you use daily, drop the port. `--stdio` puts one session on
stdin and stdout instead of a listener, which is what ssh's `ProxyCommand`
expects, so ssh starts and stops the tunnel itself:

```
Host target-01
    User you
    ProxyCommand beacon forward %h ssh --stdio
    ForwardAgent yes
```

`%h` is the host you typed, so naming the block after the agent id lets one
entry cover several machines: list them on the `Host` line, or match them with
a pattern. Then `ssh target-01` works with no second terminal, and so does
everything built on ssh: `scp`, `rsync`, `git`, VS Code Remote-SSH, JetBrains
Gateway.

`ForwardAgent yes` is what keeps your git credentials out of the customer's
environment. The key stays on your laptop and only signatures cross the tunnel,
so `git push` from the target machine authenticates as you without a token ever
being written there — and the traffic still leaves from inside their network.

> [!WARNING]
> Agent forwarding lends your key for the length of the session: anyone with
> root on the target can use the socket while you are connected. Leave it off
> for a machine you do not trust that far.

### Remote development

This is the route that replaces working through a desktop. The files, the
toolchain and the build stay on the target machine, with its view of the
customer's git server and their internal services; the editor and the keyboard
stay on your own laptop.

There are two routes to it, and which one you want turns on a single question:
does anything on the target need to act as *you*?

| | `dev` — the agent's own server | `ssh` — the target's sshd |
| --- | --- | --- |
| Needed on the target | Nothing beyond the agent | sshd, an account, your key in it |
| Works on a stock Windows box | Yes | Only with the OpenSSH Server feature added |
| Who authenticates | The relay already did | The target's own sshd |
| Agent forwarding, so `git push` uses your key | **No** | Yes |

Start with `dev`. Move to `ssh` when you hit the last row — pushing to the
customer's git server from the remote terminal with the key on your laptop is
the usual reason, and it is not a small one.

#### The short route: the agent's own server

Nothing to install or configure on the target. The agent answers SSH itself.

```sh
beacon forward target-01 dev --listen 127.0.0.1:2222
ssh -p 2222 dev@127.0.0.1
```

Or as a `~/.ssh/config` entry, so editors find it:

```
Host target-01-dev
    User dev
    ProxyCommand beacon forward %h dev --stdio
```

The username is not a credential — the tunnel already authenticated an operator
— so it carries a choice instead. `dev@` is the machine's own shell; `wsl@` is a
shell inside WSL where the agent can reach one.

Point VS Code's **Remote - SSH** at `target-01-dev` and everything runs on the
target, including SFTP, which is what lets the editor actually open files.

> [!NOTE]
> `dev` and `ssh` are separate services on purpose. Substituting one for the
> other would change who authenticates without saying so.

#### The full route: the target's own sshd

This is the one that carries your identity onto the machine. It is the
`ProxyCommand` entry from the previous section plus an editor, so set that up
first and confirm plain `ssh target-01` works. Everything below assumes it does.

**On the target machine**

| What | How |
| --- | --- |
| The agent | `beacon install` then `beacon enroll`, as in [Installing on a target machine](#installing-on-a-target-machine) |
| An sshd | Linux: usually already running. macOS: **Remote Login**, in Settings → General → Sharing. Windows: the **OpenSSH Server** optional feature |
| An account, holding your key | Your public key in that account's `authorized_keys`. The relay does not authenticate this hop — the target's own sshd does |

If sshd listens anywhere other than port 22, say so in the agent's config rather
than in your ssh config. The caller names a service, never an address, so the
port is the target's business:

```json
{ "services": { "ssh": "127.0.0.1:2222" } }
```

**On your workstation**

Four things, once. First the binary, which `ProxyCommand` runs by name and so
has to be on `PATH`:

```sh
mkdir -p ~/bin
wget -qO ~/bin/beacon https://github.com/bolchisb/go-beacon/releases/latest/download/beacon-darwin-arm64
chmod +x ~/bin/beacon
```

Substitute the file name for your platform. [Setting up your
workstation](#setting-up-your-workstation) has the checksums, the macOS
quarantine note and the rest of the detail.

Then the relay it should talk to, and a session on it:

```sh
beacon config set server=https://relay.example.com
beacon login
```

Then the ssh entry from the previous section, in `~/.ssh/config`:

```
Host target-01
    User you
    ProxyCommand beacon forward %h ssh --stdio
    ForwardAgent yes
```

And finally the **Remote - SSH** extension in VS Code. Confirm plain
`ssh target-01` works before opening the editor: if it does not, the editor will
only tell you so more slowly.

Then run **Remote-SSH: Connect to Host** from the command palette and pick
`target-01`. VS Code opens a window whose terminal, extensions, debugger and
source control all run on the target machine, and `git push` from that terminal
leaves through the customer's network — authenticated by the key still sitting
on your laptop, because of `ForwardAgent yes`.

> [!WARNING]
> On the first connection VS Code downloads its own server, roughly 100MB, onto
> the target from `update.code.visualstudio.com`. In an environment with
> filtered egress that download is what fails, not the tunnel, and the symptom
> is a window that hangs on "Setting up SSH Host". Check it before blaming
> anything here:
>
> ```sh
> ssh target-01 'curl -sI https://update.code.visualstudio.com | head -1'
> ```

Two things worth knowing about how this behaves:

- VS Code opens several ssh connections to one host, and each one spawns its own
  `beacon forward --stdio`, so the dashboard shows several sessions for a single
  editor window. Adding `ControlMaster auto` and a `ControlPath` to the `Host`
  block collapses them into one.
- When your relay session expires, ssh fails rather than the editor: the
  `beacon` process writes its reason to stderr, which ssh passes through. Run
  `beacon login` again.

> [!NOTE]
> On a Windows target you land in a Windows shell, with that machine's
> toolchain. If the one you need lives in WSL, point the `ssh` service at the
> sshd inside the distribution instead of at the host's.

#### The other routes, from the same setup

Nothing above replaces the rest of the toolkit; it changes when you reach for
it. The editor covers the work you do by hand, and each of these covers
something it cannot.

**Files — `scp` and `rsync`.** The `Host` block is an ordinary ssh entry, so
they take the alias and nothing else. No port, no second terminal:

```sh
scp ./patch.diff target-01:/tmp/
rsync -av ./src/ target-01:/opt/app/
```

VS Code already moves a file you drag into its explorer. These are for the ones
you did not want to open: a database dump out, a vendor archive in, a directory
tree either way.

**A desktop — `beacon forward … rdp`.** A remote desktop client cannot spawn a
transport the way ssh does with `ProxyCommand`, so this route keeps the local
listener:

```sh
beacon forward target-01 rdp --listen 127.0.0.1:3390
```

Worth keeping for what has no terminal at all: an installer with a dialog, a
vendor tool that only ships a GUI, a Windows setting behind three panes.

**An assistant — the MCP endpoint.** The relay exposes its own tools, so the
assistant needs nothing local beyond being pointed at them:

```sh
claude mcp add --transport http beacon https://relay.example.com/mcp \
  --header "Authorization: Bearer $BEACON_ADMIN_TOKEN"
```

The division of labour is worth being deliberate about, because the two look
alike and are not:

| | Reaches the target by | Use it for |
| --- | --- | --- |
| An assistant in the VS Code terminal | already being on the machine | the repository you have open — it sees the real filesystem, with no tool call in between |
| The MCP endpoint | `run_command`, `read_file`, `list_dir` over the relay | machines you are **not** sitting in: a check across the fleet, a machine you have no editor window for |

`list_agents` first, then the machine id in every other call. The clipboard
tools ride the same endpoint and are the quickest way to carry a stack trace or
a connection string off the target:

> read the clipboard on target-01

> [!CAUTION]
> The MCP endpoint grants command execution on every connected machine. Read
> [Security](#security) before pointing an assistant at it.

### Remote desktop

The relay answers on one port and nothing else, so the port a desktop client
needs is opened next to that client rather than on the relay:

```sh
beacon forward target-01 rdp --listen 127.0.0.1:3390
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
claude mcp add --transport http beacon https://relay.example.com/mcp \
  --header "Authorization: Bearer $BEACON_ADMIN_TOKEN"
```

The header is the same operator token the dashboard uses. Without it the
endpoint answers `401` rather than exposing any tool.

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

> on target-01, run the test suite and show me what failed

The assistant runs the command on that machine, reads the files it needs and
reports back, without anyone opening a terminal.

> [!CAUTION]
> The MCP endpoint grants command execution on every connected machine, with no
> authentication. See [Security](#security).

### Clipboard

Two of those tools move text between machines, which is the quickest way to carry
a stack trace or a connection string across:

> read the clipboard on target-01

> [!NOTE]
> Windows and macOS always have a clipboard. A headless Linux host has none, and
> says so rather than failing obscurely.

## Roadmap

| Step | What it adds |
| --- | --- |
| Credential renewal | Assertions last 90 days and are re-issued by `beacon enroll`, which needs someone on the machine. Renewing over the tunnel the agent already holds, with a grace window on the renewal path only, would remove the visit. |
| Revocation | Refusing one agent without waiting for its assertion to expire, and without touching the rest. |
| Per-operator identity | Named operators rather than one shared account, so actions are attributable and one person can be removed without rotating for everyone. |

Mutual TLS was the original plan for agent identity and was **set aside
deliberately**. It would have required terminating TLS at the relay rather than
at the proxy in front of it, splitting the listener so browsers — which cannot
present client certificates — still worked, and taking on a certificate
lifecycle whose expiry is fleet-fatal on machines reachable only through the
tunnel they serve. Signed assertions at the application layer give the same
properties, including non-replayability, without any of that.

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
