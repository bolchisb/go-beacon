# Isolating failures between goroutines

**Date:** 2026-08-19 · **Status:** implemented

## Why

Go has no supervision. An unrecovered panic in **any** goroutine terminates the
whole process. Before this change the code had 33 goroutines and **zero**
`recover()` calls, so:

- a nil pointer while reading one agent's log would have disconnected every
  other agent on the relay
- a bad frame from one terminal would have killed an agent on a machine whose
  only remaining access is the tunnel that just died

`net/http` recovers panics raised inside request handlers, which hid how much
was actually exposed. The goroutines we start ourselves — sessions, ping loops,
stream handlers, copy pumps, the auto-update loop — were not covered by it.

## The model

`internal/supervise` gives the two shapes that were missing:

    supervise.Go("unit-name", fn)   // isolate: it dies alone and says why
    supervise.Do("unit-name", fn)   // convert a panic into an error

The unit of isolation is the goroutine. A stream is cheap: it dies and ends. A
session is not: `supervise.Do` turns a panic into an ordinary session failure,
and the reconnect loop that already existed backs off and tries again. Nothing
restarts a unit that has no supervisor, on purpose — an unowned restart loop is
how a crash becomes a crash loop.

Every unit is named, because a stack trace on its own rarely says which of five
identical pumps was the one that fell over.

## Where it is applied

| Relay | Agent |
| --- | --- |
| per-agent session | session, wrapped so failure means reconnect |
| per-agent ping loop | ping loop |
| agent-initiated streams | stream dispatch |
| shell and forward copy pumps | pty pump, control socket |
| closing a superseded session | log sink, auto-update loop, forward sessions |

## What this does not do

It does not make a panic acceptable. A recovered panic is a bug that now gets
logged with its stack and its unit name instead of taking the fleet down; it
still needs fixing. The log line is the point.

It also does not supervise the process itself. That belongs to systemd, launchd
and the SCM, which already restart the agent, and to the rollback in
[windows-auto-update](windows-auto-update.md) for the case where the new binary
is the thing that is broken.
