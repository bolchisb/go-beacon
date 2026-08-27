package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bolchisb/go-beacon/internal/protocol"
)

const testToken = "s3cr3t-operator-token"

// stub stands in for the mux so these tests exercise the gate and nothing else.
func gated(token string) http.Handler {
	return newAuth(token, newOperatorStore(nil, ""), parseProxies("")).protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("reached"))
	}))
}

func get(h http.Handler, path string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for _, m := range mutate {
		m(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestGateRefusesTheEndpointsThatExecuteCommands(t *testing.T) {
	h := gated(testToken)
	// Every one of these grants command execution on connected machines.
	for _, path := range []string{
		"/", "/ui/", "/api/server", "/api/agents", "/api/events",
		"/api/agents/vm-01/shell", "/api/agents/vm-01/forward/ssh", "/mcp",
	} {
		if got := get(h, path).Code; got != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", path, got)
		}
	}
}

func TestGateLeavesTheAgentTunnelAndHealthOpen(t *testing.T) {
	h := gated(testToken)
	// The agent path authenticates on its own terms and the healthcheck runs
	// inside the container with no credentials to offer.
	for _, path := range []string{protocol.ConnectPath, "/healthz"} {
		if got := get(h, path).Code; got != http.StatusOK {
			t.Errorf("%s: got %d, want 200", path, got)
		}
	}
}

func TestGateIsInertWhenNoTokenIsConfigured(t *testing.T) {
	// An upgrade must not lock out a deployment that never set a token.
	if got := get(gated(""), "/api/agents").Code; got != http.StatusOK {
		t.Errorf("got %d, want 200", got)
	}
}

func TestBearerTokenIsAccepted(t *testing.T) {
	w := get(gated(testToken), "/api/agents", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+testToken)
	})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
}

func TestWrongBearerTokenIsRefused(t *testing.T) {
	w := get(gated(testToken), "/api/agents", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer not-the-token")
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", w.Code)
	}
}

func TestBrowserGetsALoginPageAndAnAPIClientGetsJSON(t *testing.T) {
	h := gated(testToken)

	html := get(h, "/ui/", func(r *http.Request) { r.Header.Set("Accept", "text/html") })
	if !strings.Contains(html.Body.String(), "<form") {
		t.Error("a browser should be offered a sign-in form")
	}

	api := get(h, "/api/agents")
	if ct := api.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Errorf("a non-browser client got %q, want JSON", ct)
	}
	if api.Header().Get("WWW-Authenticate") == "" {
		t.Error("a 401 to an API client should say how to authenticate")
	}
}

func TestLoginIssuesASessionCookieThatWorks(t *testing.T) {
	a := newAuth(testToken, newOperatorStore(nil, ""), parseProxies(""))

	req := httptest.NewRequest(http.MethodPost, "/api/login",
		strings.NewReader("token="+testToken))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	a.handleLogin(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("login got %d, want 200", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookie {
		t.Fatalf("expected one %s cookie, got %v", sessionCookie, cookies)
	}
	if !cookies[0].HttpOnly {
		t.Error("the session cookie must be HttpOnly")
	}

	got := get(a.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})),
		"/api/agents", func(r *http.Request) { r.AddCookie(cookies[0]) })
	if got.Code != http.StatusOK {
		t.Fatalf("the cookie was not accepted: %d", got.Code)
	}
}

func TestLoginRefusesTheWrongToken(t *testing.T) {
	a := newAuth(testToken, newOperatorStore(nil, ""), parseProxies(""))
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader("token=wrong"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	a.handleLogin(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", w.Code)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Error("a failed login must not set a cookie")
	}
}

func TestATamperedOrExpiredSessionIsRejected(t *testing.T) {
	a := newAuth(testToken, newOperatorStore(nil, ""), parseProxies(""))

	if a.validSession("not-a-session") {
		t.Error("garbage accepted")
	}
	// Right shape, wrong signature.
	future := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	if a.validSession(future + ".0000000000000000000000000000000000000000000000000000000000000000") {
		t.Error("a forged signature was accepted")
	}
	// Correct signature over an expiry that has passed.
	if a.validSession(a.session(time.Now().Add(-time.Minute))) {
		t.Error("an expired session was accepted")
	}
	if !a.validSession(a.session(time.Now().Add(time.Hour))) {
		t.Error("a valid session was rejected")
	}
}

func TestRotatingTheTokenInvalidatesOutstandingSessions(t *testing.T) {
	// The signing key is the token itself, so this comes for free -- and it is
	// the only revocation mechanism a stateless server has.
	old := newAuth(testToken, newOperatorStore(nil, ""), parseProxies("")).session(time.Now().Add(time.Hour))
	if newAuth("a-different-token", newOperatorStore(nil, ""), parseProxies("")).validSession(old) {
		t.Error("a session survived a token rotation")
	}
}

func TestServerRoutesAreGated(t *testing.T) {
	// The same check against the real mux, so a future route added outside the
	// gate is caught here rather than in production.
	h := newServer(Config{Listen: ":0", AdminToken: testToken}).routes()
	for _, path := range []string{"/api/server", "/api/agents", "/mcp", "/ui/"} {
		if got := get(h, path).Code; got != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", path, got)
		}
	}
	if got := get(h, "/healthz").Code; got != http.StatusOK {
		t.Errorf("/healthz: got %d, want 200", got)
	}
}

func timeNowPlusHour() time.Time { return time.Now().Add(time.Hour) }

// TestAnUnsetAdminTokenNeverMatches is the regression for the shape that looked
// safe and was not: an operator account exists, so the gate reports itself as
// enabled and nothing warns, but the admin token was dropped from the
// deployment after bootstrap. subtle.ConstantTimeCompare calls two empty inputs
// a match, so an empty candidate authenticated as the admin.
func TestAnUnsetAdminTokenNeverMatches(t *testing.T) {
	ops := newOperatorStore(nil, "")
	ops.rec = &operator{Username: "operator", SessionKey: "a-session-key"}

	a := newAuth("", ops, parseProxies(""))
	if !a.enabled() {
		t.Fatal("an operator account should keep the gate enabled")
	}
	if a.matches("") {
		t.Fatal("an empty candidate must not match an unset admin token")
	}
	if a.matches("anything") {
		t.Fatal("nothing should match an unset admin token")
	}

	// The reachable form of it: a bearer header with no value.
	r := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	r.Header.Set("Authorization", "Bearer ")
	if a.authenticated(r) {
		t.Fatal("an empty bearer token authenticated against an unset admin token")
	}
}
