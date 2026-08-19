package main

import (
	"flag"
	"fmt"
	"strings"
)

// cmdConfig shows the effective settings and, crucially, where each value came
// from. Without the source column this command would just be `cat`.
func cmdConfig(args []string) error {
	if len(args) > 0 && args[0] == "set" {
		return configSet(args[1:])
	}

	fs := flag.NewFlagSet("beacon config", flag.ExitOnError)
	fs.Usage = func() {
		usageFor(fs, "beacon config [set KEY=VALUE ...]", "Show or change the stored settings.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	r, err := loadConfig(nil)
	if err != nil {
		return err
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
