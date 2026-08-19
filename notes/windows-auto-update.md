# Updating the agent on Windows

**Date:** 2026-08-19 · **Status:** implemented

`beacon update` and the planned hourly auto-update both assume a shape that
Windows does not have. This is what actually applies there.

## It is two problems, not one

Replacing the file and restarting the process are separate on Windows in a way
they are not on unix. Only the second one is hard.

## 1. Replacing the binary — the current approach is right

Windows locks the image of a running process against **deletion**, not against
**renaming**. A rename of a running executable succeeds; the running process
keeps executing from the file it already opened.

That is exactly what `replaceBinary` does: write `beacon.exe.new`, rename the
running `beacon.exe` to `beacon.exe.old`, rename `.new` into place. The `.old`
file cannot be deleted while its process lives, so it is removed at the start of
the next update instead.

Sources: [MoveFileEx](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-movefileexa),
[Holgate, rename vs delete](https://lenholgate.com/blog/2022/09/allowing-renaming-but-not-deleting-for-files-on-windows.html).

**Not yet verified on a real Windows host.** The mechanism is documented, the
code path is not exercised.

## 2. Restarting the service — this is what does not fit

A service cannot cleanly restart itself. Asking the SCM to stop the service you
are running in means asking it to stop you, mid-call.

### Option A — let the SCM restart it

Configure failure actions (already done: restart after 5s) and exit non-zero.

Blocked by a documented detail: recovery actions run on a clean
`SERVICE_STOPPED` with a non-zero exit code **only** when
`fFailureActionsOnNonCrashFailures` is TRUE, and

> The change takes effect the next time the system is started.

So a freshly installed agent cannot use this until the machine reboots.
`SetRecoveryActionsOnNonCrashFailures` exists in `x/sys/windows/svc/mgr`, and
install should set it regardless — it costs nothing and becomes available later.

Source: [SERVICE_FAILURE_ACTIONS_FLAG](https://learn.microsoft.com/en-us/windows/win32/api/winsvc/ns-winsvc-service_failure_actions_flag).

### Option B — terminate without reporting SERVICE_STOPPED

Works with the default flag, because an unreported termination always counts as
a crash. It is also a lie: every update writes a service-crash entry to the
event log, which is the log an administrator reads when hunting a real problem.

### Option C — a detached helper (recommended)

The agent replaces the binary, spawns a **detached** process, and returns. The
helper is not the service, so it can stop and start it:

    sc stop beacon  →  wait  →  sc start beacon

It works immediately, needs no flag and no reboot, and — the reason to prefer it
— the helper outlives the restart, so it is the only place a rollback can live.

## 3. Rollback has nowhere else to live

A bad release is fleet-fatal: the tunnel is the only way back into these
machines. On unix the updating process can wait and watch. On Windows it cannot,
because it is the thing being restarted.

The helper closes that hole:

1. stop the service
2. start it
3. wait up to N minutes for the agent to answer its control socket
4. if it does not, rename `beacon.exe.old` back and start again

Without step 4, a binary that starts and never connects leaves the machine
unreachable, and the SCM happily restarts it forever.

## 4. `beacon update` run by hand updates the wrong file

It replaces `os.Executable()`. Run from a copy in Downloads, it updates that
copy and leaves the service on the old binary — with no error, because nothing
went wrong. It should resolve the installed path when a service is registered,
or refuse and say so.

## What was built

| | Where |
| --- | --- |
| `SetRecoveryActionsOnNonCrashFailures(true)` at install | `service_windows.go` |
| detached helper: stop, start, verify, roll back | `updateapply.go`, `detach_*.go` |
| update the installed binary, not the running copy | `updateTarget()` |
| hourly loop with jitter, skipped while streams are open | `autoUpdateLoop()` |

Two decisions worth recording:

**The helper is used on every platform, not only Windows.** `systemctl restart`
issued from inside the unit has the same shape of problem: systemd stops the
caller while it is waiting for the command to return. One mechanism is easier to
reason about than two.

**Verification asks only that the agent answer its control socket**, not that it
be connected to the relay. Requiring a connection would roll back a perfectly
good build whenever the relay happened to be down, which is the moment least
able to survive an unnecessary rollback.

## Still open

Nothing verifies that a release is *authentic*. `SHA256SUMS` ships from the same
release as the binary, so it catches a truncated download and nothing else. That
matters more now than it did before this was automatic: an hourly, unattended
update is a fine place to serve a forged build from. Signing needs a public key
compiled into the agent and a signing step in the release.
