package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
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
	token []byte
}

func newAuth(token string) *auth {
	if token == "" {
		return &auth{}
	}
	return &auth{token: []byte(token)}
}

func (a *auth) enabled() bool { return len(a.token) > 0 }

// open lists the paths that must work without an operator session. The default
// is the other way round -- anything not named here is protected -- so a route
// added later is closed until someone deliberately opens it.
func (a *auth) open(path string) bool {
	switch path {
	case protocol.ConnectPath, "/healthz", "/api/login", "/api/logout":
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
			a.serveLogin(w, http.StatusUnauthorized, "")
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
	m := hmac.New(sha256.New, a.token)
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
	token := strings.TrimSpace(r.FormValue("token"))
	if !a.matches(token) {
		// Same delay whether the token was empty or merely wrong, and no hint
		// about which. Slow enough to make guessing pointless over a network,
		// fast enough that a human notices nothing.
		time.Sleep(500 * time.Millisecond)
		if wantsHTML(r) {
			a.serveLogin(w, http.StatusUnauthorized, "That token was not accepted.")
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
		return
	}

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

	if wantsHTML(r) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
		a.serveLogin(w, http.StatusOK, "Signed out.")
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

func (a *auth) serveLogin(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	note := ""
	if message != "" {
		note = `<p class="note">` + message + `</p>`
	}
	fmt.Fprintf(w, loginPage, note)
}

// Deliberately self-contained: no asset from /ui is served before sign-in.
const loginPage = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>beacon</title>
<style>
 :root { color-scheme: light dark; }
 body { font: 15px/1.5 system-ui, sans-serif; display: grid; place-items: center;
        min-height: 100vh; margin: 0; }
 form { display: grid; gap: .75rem; width: min(22rem, 90vw); }
 h1 { font-size: 1.1rem; margin: 0; font-weight: 600; }
 input { font: inherit; padding: .5rem .6rem; border: 1px solid #8888;
         border-radius: 6px; background: transparent; color: inherit; }
 button { font: inherit; padding: .5rem; border: 0; border-radius: 6px;
          background: #2563eb; color: #fff; cursor: pointer; }
 .note { color: #b91c1c; margin: 0; font-size: .9rem; }
</style>
<form method="post" action="/api/login">
  <h1>beacon</h1>
  %s
  <input type="password" name="token" placeholder="Operator token" autofocus
         autocomplete="current-password">
  <button type="submit">Sign in</button>
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
