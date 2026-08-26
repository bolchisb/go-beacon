package main

// wslState is what the agent reports about WSL on this machine.
//
// Four answers, and the difference between the middle two is the point:
//
//	available   — distributions are reachable from where this agent runs
//	unreachable — WSL is installed, but not visible from this account
//	absent      — not installed, or not Windows
//	unavailable — wsl.exe is there but did not answer
//
// "unreachable" is the common case on a real deployment and the one that would
// otherwise look like a bug: the agent runs as a service under LocalSystem, and
// WSL distributions belong to user accounts.
type wslState struct {
	Status  string   `json:"status"`
	Detail  string   `json:"detail,omitempty"`
	Distros []string `json:"distros,omitempty"`
}

func (w wslState) usable() bool { return w.Status == "available" }
