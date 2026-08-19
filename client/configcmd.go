package main

import (
	"flag"
	"fmt"
	"strings"
)

// cmdConfig shows the effective settings and, crucially, where each value came
// from. Without the source column this command would just be `cat`.
func cmdConfig(args []string) error {
	showOnly := false
	if len(args) > 0 {
		switch args[0] {
		case "set":
			return configSet(args[1:])
		case "show":
			showOnly, args = true, args[1:]
		}
	}

	fs := flag.NewFlagSet("beacon config", flag.ExitOnError)
	fs.Usage = func() {
		usageFor(fs, "beacon config [show | set KEY=VALUE ...]",
			"Edit the settings interactively, or show them.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	r, err := loadConfig(nil)
	if err != nil {
		return err
	}

	// the editor needs a terminal on both ends; piped or scripted, this stays
	// the read-only view it has always been
	if !showOnly && isInteractive() {
		return runConfigForm(r)
	}

	p := &panel{title: "config", right: version}
	p.blank()
	for _, key := range configKeys {
		value := r.value(key)
		if value == "" {
			value = "not set"
		}
		p.line(styLabel.Render(fixed(key, labelWidth)) +
			styValue.Render(fixed(value, 30)) +
			styDim.Render(string(r.sources[key])))
	}
	p.blank()
	if r.exists {
		p.footer = r.path
	} else {
		p.footer = r.path + " (does not exist)"
	}

	fmt.Print(p.render())
	return nil
}

func configSet(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: beacon config set KEY=VALUE ...  (keys: %s)", strings.Join(configKeys, ", "))
	}

	// only the file is edited: folding in env or flag values would silently
	// freeze something the operator meant to stay dynamic
	current, _, err := readConfigFile(configPath())
	if err != nil {
		return err
	}

	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok {
			return fmt.Errorf("expected KEY=VALUE, got %q", arg)
		}
		switch key {
		case keyServer:
			current.Server = value
		case keyID:
			current.AgentID = value
		case keyCA:
			current.CAFile = value
		default:
			return fmt.Errorf("unknown key %q (keys: %s)", key, strings.Join(configKeys, ", "))
		}
	}

	path, err := saveConfig(current)
	if err != nil {
		return err
	}
	fmt.Printf("%s written\n", path)
	return nil
}
