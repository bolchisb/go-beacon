package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bolchisb/go-beacon/internal/protocol"
)

// Operator authentication. This gate is about humans, not agents: the dashboard,
// the API and the MCP endpoint all grant command execution on every connected
// machine, so they are the surface that matters most. Agents authenticate
// separately on their own path.
//
// There is no session store because the server has none. A cookie carries its
// own expiry and an HMAC over it, so a restart does not sign everyone out and
// nothing has to be persisted.

const (
	sessionCookie = "beacon_session"
	sessionTTL    = 12 * time.Hour
)

type auth struct {
	// token is nil when BEACON_ADMIN_TOKEN is unset, which leaves the relay
	// open. That is the behaviour every existing deployment already has, so
	// upgrading does not lock anyone out -- but it is announced loudly at
	// startup and reported through /api/server.
	//
	// Once an operator account exists the token stops being the everyday
	// credential and becomes two other things: what authorises creating that
	// account in the first place, and what gets you back in when both Vault and
	// its cache are gone.
	token []byte

	ops *operatorStore
}

func newAuth(token string, ops *operatorStore) *auth {
	a := &auth{ops: ops}
	if token != "" {
		a.token = []byte(token)
	}
	return a
}

func (a *auth) enabled() bool {
	return len(a.token) > 0 || (a.ops != nil && a.ops.exists())
}

// bootstrapping reports whether the dashboard should be asking for the admin
// token and a new username and password rather than for a login.
func (a *auth) bootstrapping() bool {
	return a.ops == nil || !a.ops.exists()
}

// sessionKey signs session cookies. The operator's own key once there is an
// account, the admin token before that, so cookies work either side of setup.
// A consequence worth knowing: changing the password with session rotation, or
// rotating the admin token, invalidates every outstanding session.
func (a *auth) sessionKey() []byte {
	if a.ops != nil {
		if rec := a.ops.current(); rec != nil && rec.SessionKey != "" {
			return []byte(rec.SessionKey)
		}
	}
	return a.token
}

// open lists the paths that must work without an operator session. The default
// is the other way round -- anything not named here is protected -- so a route
// added later is closed until someone deliberately opens it.
func (a *auth) open(path string) bool {
	switch path {
	case protocol.ConnectPath, "/healthz", "/api/login", "/api/logout", "/api/bootstrap":
		return true
	}
	return false
}

func (a *auth) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.enabled() || a.open(r.URL.Path) || a.authenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		// A browser gets something it can act on; anything else gets 401, so a
		// CLI or an assistant sees a status code rather than a login page.
		if wantsHTML(r) {
			a.serveSignIn(w, http.StatusUnauthorized, "")
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="beacon"`)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
	})
}

func (a *auth) authenticated(r *http.Request) bool {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if a.matches(strings.TrimPrefix(h, "Bearer ")) {
			return true
		}
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	return a.validSession(c.Value)
}

func (a *auth) matches(candidate string) bool {
	return subtle.ConstantTimeCompare([]byte(candidate), a.token) == 1
}

// session mints "<expiry>.<hmac>". The key is the admin token itself, so
// rotating the token invalidates every outstanding session for free.
func (a *auth) session(expiry time.Time) string {
	exp := strconv.FormatInt(expiry.Unix(), 10)
	return exp + "." + a.sign(exp)
}

func (a *auth) sign(msg string) string {
	m := hmac.New(sha256.New, a.sessionKey())
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

func (a *auth) validSession(v string) bool {
	exp, mac, ok := strings.Cut(v, ".")
	if !ok {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(mac), []byte(a.sign(exp))) != 1 {
		return false
	}
	unix, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Before(time.Unix(unix, 0))
}

func (a *auth) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !a.enabled() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "authentication is disabled"})
		return
	}
	// Before an account exists there is nothing to log in to.
	if a.bootstrapping() && strings.TrimSpace(r.FormValue("token")) == "" {
		a.serveSignIn(w, http.StatusUnauthorized, "")
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	token := strings.TrimSpace(r.FormValue("token"))

	ok := false
	switch {
	case token != "":
		// The recovery path: the admin token still signs you in, which is what
		// you reach for when the password is lost or Vault is unavailable.
		ok = a.matches(token)
	case a.ops != nil:
		ok = a.ops.verify(username, password)
	}

	if !ok {
		// The same delay and the same wording whichever field was wrong, so
		// neither a valid username nor a valid token can be found by probing.
		time.Sleep(500 * time.Millisecond)
		if wantsHTML(r) {
			a.serveSignIn(w, http.StatusUnauthorized, "Those credentials were not accepted.")
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	a.setSession(w, r)
	if wantsHTML(r) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleBootstrap creates the first operator account. It is authorised by the
// admin token, which the relay already requires in order to start at all --
// so nothing new has to be distributed, no password is written to a log, and
// there is no window in which whoever reaches the page first owns the relay.
func (a *auth) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	if !a.bootstrapping() {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "an operator account already exists; change the password instead"})
		return
	}
	if !a.enabled() {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "set an admin token before creating an operator account"})
		return
	}

	if !a.matches(strings.TrimSpace(r.FormValue("token"))) {
		time.Sleep(500 * time.Millisecond)
		a.fail(w, r, http.StatusUnauthorized, "That admin token was not accepted.")
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	if password != r.FormValue("password_confirm") {
		a.fail(w, r, http.StatusBadRequest, "The two passwords do not match.")
		return
	}
	if err := a.ops.save(username, password, true); err != nil {
		a.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	slog.Info("operator account created", "user", username)
	a.setSession(w, r)
	if wantsHTML(r) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "username": username})
}

// handleChangePassword runs behind the gate, so the caller is already signed in.
// The current password is still required: a borrowed session should not be
// enough to lock the real operator out.
func (a *auth) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if a.ops == nil || !a.ops.exists() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no operator account yet"})
		return
	}
	rec := a.ops.current()

	current := r.FormValue("current_password")
	if !a.ops.verify(rec.Username, current) && !a.matches(strings.TrimSpace(current)) {
		time.Sleep(500 * time.Millisecond)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "the current password is wrong"})
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	if username == "" {
		username = rec.Username
	}
	password := r.FormValue("password")
	if password != r.FormValue("password_confirm") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the two passwords do not match"})
		return
	}
	// Rotating the session key signs every other session out, which is the
	// point of changing a password you think someone else knows.
	if err := a.ops.save(username, password, true); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	slog.Info("operator password changed", "user", username)
	a.setSession(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "username": username})
}

func (a *auth) fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if wantsHTML(r) {
		a.serveSignIn(w, status, msg)
		return
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

func (a *auth) setSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    a.session(time.Now().Add(sessionTTL)),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Secure is set only when the request arrived over TLS: a relay reached
		// on plain http during development would otherwise set a cookie the
		// browser refuses to send back.
		Secure:  isTLS(r),
		Expires: time.Now().Add(sessionTTL),
	})
}

func (a *auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isTLS(r),
		MaxAge:   -1,
	})
	if wantsHTML(r) {
		a.serveSignIn(w, http.StatusOK, "Signed out.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// isTLS reports whether the browser reached us over https, including the common
// case of a reverse proxy that terminated TLS and said so.
func isTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// serveSignIn renders whichever of the two forms applies: first-run setup while
// there is no account, an ordinary login once there is one.
func (a *auth) serveSignIn(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	note := ""
	if message != "" {
		note = `<p class="note">` + html.EscapeString(message) + `</p>`
	}
	if a.bootstrapping() {
		fmt.Fprintf(w, pageShell, "Set up beacon", note, bootstrapFields)
		return
	}
	fmt.Fprintf(w, pageShell, "beacon", note, loginFields)
}

// Deliberately self-contained: no asset from /ui is served before sign-in.
const pageShell = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>beacon</title>
<style>
 :root { color-scheme: light dark; }
 body { font: 15px/1.5 system-ui, sans-serif; display: grid; place-items: center;
        min-height: 100vh; margin: 0; }
 form { display: grid; gap: .75rem; width: min(22rem, 90vw); }
 h1 { font-size: 1.1rem; margin: 0; font-weight: 600; }
 p.hint { margin: 0; font-size: .85rem; opacity: .75; }
 input { font: inherit; padding: .5rem .6rem; border: 1px solid #8888;
         border-radius: 6px; background: transparent; color: inherit; }
 button { font: inherit; padding: .5rem; border: 0; border-radius: 6px;
          background: #2563eb; color: #fff; cursor: pointer; }
 .note { color: #b91c1c; margin: 0; font-size: .9rem; }
 details { font-size: .85rem; opacity: .8; }
 summary { cursor: pointer; }
</style>
<h1>%s</h1>
%s
%s
`

const loginFields = `<form method="post" action="/api/login">
  <input name="username" placeholder="Username" autofocus autocomplete="username">
  <input type="password" name="password" placeholder="Password"
         autocomplete="current-password">
  <button type="submit">Sign in</button>
</form>
<details>
  <summary>Lost the password?</summary>
  <form method="post" action="/api/login">
    <p class="hint">Sign in with the admin token from the relay host, then
       change the password.</p>
    <input type="password" name="token" placeholder="Admin token">
    <button type="submit">Sign in with token</button>
  </form>
</details>
`

const bootstrapFields = `<form method="post" action="/api/bootstrap">
  <p class="hint">No operator account exists yet. Authorise with the admin token
     from the relay host, then choose the credentials you will use from now on.</p>
  <input type="password" name="token" placeholder="Admin token" autofocus>
  <input name="username" placeholder="Choose a username" autocomplete="username">
  <input type="password" name="password" placeholder="Choose a password (12+ recommended)"
         autocomplete="new-password">
  <input type="password" name="password_confirm" placeholder="Repeat the password"
         autocomplete="new-password">
  <button type="submit">Create account</button>
</form>
`

// warnIfOpen makes an unauthenticated relay impossible to run by accident.
func (a *auth) warnIfOpen() {
	if a.enabled() {
		return
	}
	slog.Warn("BEACON_ADMIN_TOKEN is not set: the dashboard, the API and the MCP " +
		"endpoint are open to anyone who can reach this relay, and all three grant " +
		"command execution on every connected machine")
}
