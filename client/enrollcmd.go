package main

import "flag"

// beacon enroll gives this machine an identity with the relay, without
// touching the installed service.
//
// It exists as its own command because enrolment is not a one-off part of
// installing: assertions expire, a relay can be rebuilt, and a machine can be
// pointed at a different one. Reinstalling a service to refresh a credential is
// the wrong shape, and it is what someone reaches for when the only way to
// enrol is `beacon install`.
func cmdEnroll(args []string) error {
	fs := flag.NewFlagSet("beacon enroll", flag.ExitOnError)
	fs.String(keyServer, "", "relay URL, http:// or https://")
	fs.String(keyID, "", "agent identity shown in the dashboard")
	fs.String(keyCA, "", "PEM bundle trusted in addition to the system roots")
	fs.String(keyUser, "", "operator username to enrol with")
	fs.Usage = func() {
		usageFor(fs, "beacon enroll", "Give this machine an identity with the relay.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadConfig(explicitFlags(fs))
	if err != nil {
		return err
	}
	if err := requireElevation(); err != nil {
		return err
	}
	if err := enrollThisMachine(cfg); err != nil {
		return err
	}
	if _, err := saveConfig(cfg.Config); err != nil {
		return err
	}

	p := resultPanel("enroll", markDone, styOK, "ENROLLED", "")
	p.kv("agent id", cfg.AgentID)
	p.kv("relay", cfg.Server)
	p.kv("config", configPath())
	// Restarting is the operator's call: the service may be mid-session, and
	// the agent picks the new assertion up on its next reconnect anyway.
	p.footer = "beacon restart"
	p.show()
	return nil
}
