package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const goodPassword = "correct-horse-battery"

func TestPasswordHashRoundTrip(t *testing.T) {
	ph, err := hashPassword(goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ph, goodPassword) {
		t.Fatal("the hash contains the password")
	}

	ok, err := verifyPassword(ph, goodPassword)
	if err != nil || !ok {
		t.Fatalf("the correct password did not verify: ok=%v err=%v", ok, err)
	}
	if ok, _ := verifyPassword(ph, goodPassword+"x"); ok {
		t.Error("a wrong password verified")
	}

	// Two hashes of the same password must differ, or the salt is not doing
	// its job and identical passwords become visible to anyone reading Vault.
	other, _ := hashPassword(goodPassword)
	if other == ph {
		t.Error("hashing is not salted")
	}
}

func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	for _, h := range []string{
		"", "plaintext", "$argon2i$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=1,t=1,p=1$not-base64!$aGFzaA",
	} {
		if ok, err := verifyPassword(h, goodPassword); ok || err == nil {
			t.Errorf("accepted malformed hash %q (ok=%v err=%v)", h, ok, err)
		}
	}
}

func TestValidateCredentials(t *testing.T) {
	if err := validateCredentials("", goodPassword); err == nil {
		t.Error("an empty username was accepted")
	}
	if err := validateCredentials("admin", ""); err == nil {
		t.Error("an empty password was accepted")
	}
	// Short is allowed on purpose; empty is not.
	if err := validateCredentials("admin", "x"); err != nil {
		t.Errorf("a short password was rejected: %v", err)
	}
	if err := validateCredentials("admin", goodPassword); err != nil {
		t.Errorf("a reasonable pair was rejected: %v", err)
	}
}

// fakeVault stands in for a Vault with the KV v2 engine mounted, which is
// enough to exercise the data.data nesting that is easy to get wrong and fails
// silently when you do.
func fakeVault(t *testing.T) (*vault, map[string]json.RawMessage) {
	t.Helper()
	store := map[string]json.RawMessage{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/v1/"+kvMount+"/data/")
		switch r.Method {
		case http.MethodPost:
			var body struct {
				Data json.RawMessage `json:"data"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			store[key] = body.Data
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			raw, ok := store[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":[]}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"data": json.RawMessage(raw)},
			})
		}
	}))
	t.Cleanup(srv.Close)

	return &vault{addr: srv.URL, staticTok: "test", token: "test", httpc: srv.Client()}, store
}

func TestOperatorSurvivesARestart(t *testing.T) {
	v, _ := fakeVault(t)
	dir := t.TempDir()

	first := newOperatorStore(v, dir)
	if first.exists() {
		t.Fatal("a fresh store should have no account")
	}
	if err := first.save("alice", goodPassword, true); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	// A new relay process against the same Vault.
	second := newOperatorStore(v, t.TempDir())
	second.load()
	if !second.exists() {
		t.Fatal("the account did not come back from Vault")
	}
	if !second.verify("alice", goodPassword) {
		t.Error("the password did not verify after a restart")
	}
	if second.verify("alice", "wrong-password-entirely") {
		t.Error("a wrong password verified")
	}
	if second.verify("bob", goodPassword) {
		t.Error("a wrong username verified")
	}
}

func TestOperatorLoadsFromCacheWhenVaultIsGone(t *testing.T) {
	// The property the whole cache exists for: a sealed Vault must not lock an
	// operator out of the dashboard.
	v, _ := fakeVault(t)
	dir := t.TempDir()

	live := newOperatorStore(v, dir)
	if err := live.save("alice", goodPassword, true); err != nil {
		t.Fatal(err)
	}

	dead := newOperatorStore(&vault{addr: "http://127.0.0.1:1", httpc: http.DefaultClient}, dir)
	dead.load()
	if !dead.exists() {
		t.Fatal("the account was not recovered from the cache")
	}
	if !dead.verify("alice", goodPassword) {
		t.Error("the cached account did not verify")
	}
}

func TestSavingRefusesWithoutSomewhereDurable(t *testing.T) {
	// A password that appears to change but does not survive a restart is worse
	// than one that refuses to change.
	s := newOperatorStore(nil, t.TempDir())
	if err := s.save("alice", goodPassword, true); err == nil {
		t.Fatal("saving succeeded with no Vault configured")
	}
}

func TestChangingThePasswordRotatesSessions(t *testing.T) {
	v, _ := fakeVault(t)
	ops := newOperatorStore(v, t.TempDir())
	if err := ops.save("alice", goodPassword, true); err != nil {
		t.Fatal(err)
	}
	a := newAuth(testToken, ops)

	old := a.session(timeNowPlusHour())
	if !a.validSession(old) {
		t.Fatal("a fresh session did not validate")
	}
	if err := ops.save("alice", "another-good-password", true); err != nil {
		t.Fatal(err)
	}
	if a.validSession(old) {
		t.Error("a session survived a password change")
	}
}

func TestBootstrapNeedsTheAdminTokenAndCreatesTheAccount(t *testing.T) {
	v, _ := fakeVault(t)
	ops := newOperatorStore(v, t.TempDir())
	a := newAuth(testToken, ops)

	if !a.bootstrapping() {
		t.Fatal("a relay with no account should be in setup")
	}

	post := func(form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/bootstrap", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		a.handleBootstrap(w, r)
		return w
	}

	if got := post(url.Values{
		"token": {"wrong"}, "username": {"alice"},
		"password": {goodPassword}, "password_confirm": {goodPassword},
	}).Code; got != http.StatusUnauthorized {
		t.Errorf("setup without the admin token returned %d, want 401", got)
	}
	if a.ops.exists() {
		t.Fatal("an account was created despite a bad token")
	}

	if got := post(url.Values{
		"token": {testToken}, "username": {"alice"},
		"password": {goodPassword}, "password_confirm": {"different"},
	}).Code; got != http.StatusBadRequest {
		t.Errorf("mismatched passwords returned %d, want 400", got)
	}

	w := post(url.Values{
		"token": {testToken}, "username": {"alice"},
		"password": {goodPassword}, "password_confirm": {goodPassword},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("setup returned %d, want 200", w.Code)
	}
	if !ops.verify("alice", goodPassword) {
		t.Error("the account was not usable after setup")
	}
	if a.bootstrapping() {
		t.Error("still in setup after an account was created")
	}

	// Setup must close behind itself, or anyone could replace the account.
	if got := post(url.Values{
		"token": {testToken}, "username": {"mallory"},
		"password": {goodPassword}, "password_confirm": {goodPassword},
	}).Code; got != http.StatusConflict {
		t.Errorf("a second setup returned %d, want 409", got)
	}
}

func TestLoginAcceptsThePasswordAndTheRecoveryToken(t *testing.T) {
	v, _ := fakeVault(t)
	ops := newOperatorStore(v, t.TempDir())
	if err := ops.save("alice", goodPassword, true); err != nil {
		t.Fatal(err)
	}
	a := newAuth(testToken, ops)

	login := func(form url.Values) int {
		r := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		a.handleLogin(w, r)
		return w.Code
	}

	if got := login(url.Values{"username": {"alice"}, "password": {goodPassword}}); got != http.StatusOK {
		t.Errorf("password login returned %d, want 200", got)
	}
	if got := login(url.Values{"username": {"alice"}, "password": {"nope"}}); got != http.StatusUnauthorized {
		t.Errorf("a wrong password returned %d, want 401", got)
	}
	// The way back in when the password is lost.
	if got := login(url.Values{"token": {testToken}}); got != http.StatusOK {
		t.Errorf("token recovery returned %d, want 200", got)
	}
	if got := login(url.Values{"token": {"wrong"}}); got != http.StatusUnauthorized {
		t.Errorf("a wrong token returned %d, want 401", got)
	}
}
