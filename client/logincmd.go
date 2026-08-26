package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"golang.org/x/term"
)

// beacon login exchanges the operator's username and password for a session.
//
// The password is read once and never written anywhere: what lands in the
// config file is the session cookie the relay issued for it. That cookie has
// its own expiry, so a stolen workstation config goes stale on its own, and it
// is not the relay's admin token -- which is also the recovery credential and
// has no business being copied onto every machine that wants a shell.
func cmdLogin(args []string) error {
	fs := flag.NewFlagSet("beacon login", flag.ExitOnError)
	fs.String(keyServer, "", "relay URL, http:// or https://")
	fs.String(keyCA, "", "PEM bundle trusted in addition to the system roots")
	fs.String(keyUser, "", "operator username; remembered for next time")
	fs.Usage = func() {
		usageFor(fs, "beacon login", "Sign in to the relay and remember the session.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadConfig(explicitFlags(fs))
	if err != nil {
		return err
	}

	username := cfg.Username
	if username == "" {
		if username, err = prompt("Username: "); err != nil {
			return err
		}
	} else {
		fmt.Printf("Username: %s\n", username)
	}

	password, err := promptPassword("Password: ")
	if err != nil {
		return err
	}

	session, err := login(cfg.Server, cfg.CAFile, username, password)
	if err != nil {
		return err
	}

	saved := cfg.Config
	saved.Username = username
	saved.Session = session
	path, err := saveConfig(saved)
	if err != nil {
		return err
	}

	fmt.Printf("Signed in as %s. Session saved to %s\n", username, path)
	return nil
}

// cmdLogout forgets the session locally. The relay is stateless about sessions,
// so there is nothing to revoke there; changing the password is what actually
// invalidates one everywhere.
func cmdLogout(args []string) error {
	cfg, err := loadConfig(nil)
	if err != nil {
		return err
	}
	if cfg.Session == "" {
		fmt.Println("Not signed in.")
		return nil
	}
	saved := cfg.Config
	saved.Session = ""
	path, err := saveConfig(saved)
	if err != nil {
		return err
	}
	fmt.Printf("Session forgotten. %s\n", path)
	return nil
}

func login(server, caFile, username, password string) (string, error) {
	tlsCfg, err := tlsConfig(caFile)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(server)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("server URL %q has no host", server)
	}
	u.Path = "/api/login"
	u.RawQuery = ""

	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
		// The session arrives as a Set-Cookie on the response; following a
		// redirect would discard the response that carries it.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	form := url.Values{"username": {username}, "password": {password}}
	resp, err := client.PostForm(u.String(), form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("the relay did not accept those credentials")
	}
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("this relay has no operator account yet; set one up in the dashboard first")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 399 {
		return "", fmt.Errorf("relay refused the sign-in: %s", resp.Status)
	}

	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			return c.Value, nil
		}
	}
	return "", fmt.Errorf("the relay accepted the sign-in but issued no session")
}

func prompt(label string) (string, error) {
	fmt.Print(label)
	var v string
	if _, err := fmt.Scanln(&v); err != nil {
		return "", err
	}
	return strings.TrimSpace(v), nil
}

func promptPassword(label string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("this needs a terminal to read a password without echoing it")
	}
	fmt.Print(label)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	return string(b), nil
}
