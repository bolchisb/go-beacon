// Command beacon is the agent side of the relay: it holds one outbound tunnel
// open to the control plane and serves whatever the server opens on it. It is
// pure Go, so one set of sources covers windows, linux and darwin on amd64 and
// arm64; only service registration differs per platform.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	// launched by the windows Service Control Manager there is no command line
	// to read, so this has to come first
	if handled, err := runUnderServiceManager(); handled {
		if err != nil {
			fail("service", err)
		}
		return
	}

	args := os.Args[1:]
	if len(args) == 0 {
		printHelp()
		return
	}

	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "run":
		err = cmdRun(rest)
	case "status":
		err = cmdStatus(rest)
	case "config":
		err = cmdConfig(rest)
	case "install":
		err = cmdInstall(rest)
	case "uninstall":
		err = cmdUninstall(rest)
	case "enroll":
		err = cmdEnroll(rest)
	case "login":
		err = cmdLogin(rest)
	case "logout":
		err = cmdLogout(rest)
	case "ssh":
		err = cmdSSH(rest)
	case "forward":
		err = cmdForward(rest)
	case "update":
		err = cmdUpdate(rest)
	case "restart-after-update": // internal, spawned by the agent
		err = cmdRestartAfterUpdate(rest)
	case "start", "stop", "restart":
		err = cmdServiceControl(cmd, rest)
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "--help", "-h":
		printHelp()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		printHelp()
		os.Exit(2)
	}
	if err != nil {
		fail(cmd, err)
	}
}

// fail reports through the same panel as every other outcome, unless the
// command already rendered its own explanation.
func fail(command string, err error) {
	if !errors.Is(err, errSilent) {
		fmt.Fprint(os.Stderr, errorPanel(command, err))
	}
	os.Exit(1)
}

type command struct{ name, summary string }

var commands = []command{
	{"status", "show whether the agent is connected"},
	{"run", "run the agent in the foreground"},
	{"install", "install the agent as a system service"},
	{"ssh", "open a terminal on a machine, in this terminal"},
	{"forward", "open a local port that leads to a service on a machine"},
	{"update", "replace this binary with the latest release"},
	{"uninstall", "remove the service and the installed binary"},
	{"start", "start the installed service"},
	{"stop", "stop the installed service"},
	{"restart", "stop then start the installed service"},
	{"config", "show settings and where each value came from"},
	{"version", "print the version"},
	{"help", "show this text"},
}

func printHelp() {
	var b strings.Builder
	b.WriteString(styTitle.Render("beacon") + styDim.Render(" "+version) + "\n")
	b.WriteString(styDim.Render("outbound agent for the go-beacon relay") + "\n\n")
	b.WriteString(styLabel.Render("USAGE") + "\n  beacon <command> [flags]\n\n")
	b.WriteString(styLabel.Render("COMMANDS") + "\n")
	for _, c := range commands {
		b.WriteString("  " + styValue.Render(fixed(c.name, 11)) + styDim.Render(c.summary) + "\n")
	}
	b.WriteString("\n" + styLabel.Render("EXAMPLES") + "\n")
	b.WriteString(styDim.Render("  beacon install --server https://relay.example.com --id build-01") + "\n")
	b.WriteString(styDim.Render("  beacon status") + "\n")
	b.WriteString(styDim.Render("  beacon ssh mm01ops") + "\n")
	b.WriteString(styDim.Render("  beacon forward mm01ops rdp --listen 127.0.0.1:3390") + "\n")
	b.WriteString(styDim.Render("  beacon config set server=https://relay.example.com") + "\n")
	b.WriteString("\n" + styLabel.Render("ENVIRONMENT") + "\n")
	b.WriteString(styDim.Render("  BEACON_SERVER, BEACON_AGENT_ID, BEACON_CA_FILE") + "\n")
	b.WriteString(styDim.Render("  BEACON_CONFIG, BEACON_SOCKET   override file locations") + "\n")
	fmt.Print(b.String())
}

// usageFor keeps every subcommand's -h looking the same.
func usageFor(fs *flag.FlagSet, use, summary string) {
	fmt.Fprintln(os.Stderr, styTitle.Render(use))
	fmt.Fprintln(os.Stderr, styDim.Render(summary))
	if hasFlags(fs) {
		fmt.Fprintln(os.Stderr, "\n"+styLabel.Render("FLAGS"))
		fs.PrintDefaults()
	}
}

func hasFlags(fs *flag.FlagSet) bool {
	found := false
	fs.VisitAll(func(*flag.Flag) { found = true })
	return found
}
