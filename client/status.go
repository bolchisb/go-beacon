package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

// cmdStatus answers the question a developer actually has: can I reach my
// machine right now, and if not, what is in the way.
func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("beacon status", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the raw payload instead of a panel")
	fs.Usage = func() { usageFor(fs, "beacon status", "Show the state of the running agent.") }
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, _, err := fetchStatus()
	if *asJSON {
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}

	// only ask the service manager when the agent itself did not answer:
	// shelling out to systemctl or launchctl on every call is wasteful
	var svc svcInfo
	svcErr := error(nil)
	if err != nil {
		svc, svcErr = serviceStatus()
	}

	p := &panel{title: "beacon", right: version}
	p.blank()

	switch {
	case err == nil && st.Connected:
		p.status(styOK, "CONNECTED", humanDuration(time.Since(st.Since)))
		p.blank()
		p.kv("agent", st.AgentID)
		p.kv("relay", st.Server)
		p.kv2("rtt", rttText(st.RTTms), "streams", fmt.Sprint(st.Streams))
		p.kv("traffic", fmt.Sprintf("%s in / %s out", humanBytes(st.BytesIn), humanBytes(st.BytesOut)))

	case err == nil:
		p.status(styWarn, "RECONNECTING", "offline for "+humanDuration(time.Since(st.Since)))
		p.blank()
		p.kv("agent", st.AgentID)
		p.kv("relay", st.Server)
		if st.NextRetry != nil {
			p.kv("next try", "in "+humanDuration(time.Until(*st.NextRetry)))
		}
		if st.LastError != "" {
			p.kv("error", st.LastError)
		}

	case svcErr == nil && svc.Installed && svc.Running:
		// the service manager thinks it is up but nothing answers the socket
		p.status(styErr, "NOT RESPONDING", "service is running")
		p.blank()
		p.kv("checked", socketPaths()[0])
		p.kv("hint", "beacon restart, then check the service log")

	case svcErr == nil && svc.Installed:
		p.status(styDim, "NOT RUNNING", "service installed, stopped")
		p.blank()
		p.kv("start it", "beacon start")

	default:
		p.status(styDim, "NOT INSTALLED", "")
		p.blank()
		p.kv("install", "beacon install --server https://relay.example.com")
		p.kv("or run", "beacon run --server https://relay.example.com")
	}

	p.blank()
	if err == nil && st.ConfigPath != "" {
		p.footer = st.ConfigPath
	} else if r, cerr := loadConfig(nil); cerr == nil && r.exists {
		p.footer = r.path
	} else {
		p.footer = "no config file"
	}

	fmt.Print(p.render())
	if err != nil || !st.Connected {
		os.Exit(1)
	}
	return nil
}

func rttText(ms *float64) string {
	if ms == nil {
		return "—"
	}
	return fmt.Sprintf("%.1f ms", *ms)
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d.Seconds())
	switch {
	case sec >= 86400:
		return fmt.Sprintf("%dd %dh", sec/86400, (sec%86400)/3600)
	case sec >= 3600:
		return fmt.Sprintf("%dh %dm", sec/3600, (sec%3600)/60)
	case sec >= 60:
		return fmt.Sprintf("%dm %ds", sec/60, sec%60)
	default:
		return fmt.Sprintf("%ds", sec)
	}
}
