package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Vault signs the assertions that let an agent prove who it is. Two things
// about this client are deliberate.
//
// Verification never calls Vault. The relay caches the transit public keys and
// checks signatures locally, so a sealed or unreachable Vault stops enrolment
// and renewal but does not disconnect a fleet that is already running. The
// fleet is exactly the set of machines nobody can reach to fix, so its access
// must not depend on Vault being awake.
//
// Every key version is cached, not only the newest. Transit stamps each
// signature with the version that produced it (vault:v1:, vault:v2:), so
// rotating the key leaves assertions signed by earlier versions verifiable
// until they expire on their own.

const vaultKeyCacheFile = "transit-public-keys.json"

type vault struct {
	addr      string
	transit   string
	stateDir  string
	httpc     *http.Client
	roleID    string
	secretID  string
	staticTok string

	mu      sync.RWMutex
	token   string
	pubKeys map[int]ed25519.PublicKey
}

func newVault(cfg Config) *vault {
	return &vault{
		addr:      strings.TrimRight(cfg.VaultAddr, "/"),
		transit:   cfg.VaultTransitKey,
		stateDir:  cfg.StateDir,
		roleID:    cfg.VaultRoleID,
		secretID:  cfg.VaultSecretID,
		staticTok: cfg.VaultToken,
		httpc:     &http.Client{Timeout: 10 * time.Second},
		pubKeys:   map[int]ed25519.PublicKey{},
	}
}

func (v *vault) configured() bool { return v.addr != "" }

// start brings the client up without making Vault a startup dependency. A
// failure here is logged and tolerated: the cached keys may still be enough to
// verify every agent, and refusing to boot would turn a Vault outage into a
// relay outage.
func (v *vault) start() {
	if !v.configured() {
		slog.Warn("no Vault configured: agents cannot be enrolled and existing " +
			"assertions cannot be verified")
		return
	}
	if err := v.loadCachedKeys(); err != nil {
		slog.Debug("no cached transit keys", "err", err)
	}
	if err := v.login(); err != nil {
		slog.Error("vault login failed", "err", err)
	}
	if err := v.refreshKeys(); err != nil {
		slog.Error("could not refresh transit keys", "err", err,
			"cached_versions", len(v.pubKeys))
		return
	}
	slog.Info("vault ready", "key", v.transit, "versions", len(v.pubKeys))
}

func (v *vault) login() error {
	if v.staticTok != "" {
		v.setToken(v.staticTok)
		return nil
	}
	if v.roleID == "" || v.secretID == "" {
		return fmt.Errorf("no vault credentials: set a token, or a role id and secret id")
	}
	var out struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	body := map[string]string{"role_id": v.roleID, "secret_id": v.secretID}
	if err := v.call(http.MethodPost, "auth/approle/login", body, &out, false); err != nil {
		return err
	}
	if out.Auth.ClientToken == "" {
		return fmt.Errorf("approle login returned no token")
	}
	v.setToken(out.Auth.ClientToken)
	return nil
}

func (v *vault) setToken(t string) {
	v.mu.Lock()
	v.token = t
	v.mu.Unlock()
}

// Sign returns a transit signature over msg, in Vault's "vault:vN:base64" form.
func (v *vault) Sign(msg []byte) (string, error) {
	if !v.configured() {
		return "", fmt.Errorf("no vault configured")
	}
	var out struct {
		Data struct {
			Signature string `json:"signature"`
		} `json:"data"`
	}
	body := map[string]string{"input": base64.StdEncoding.EncodeToString(msg)}
	if err := v.call(http.MethodPost, "transit/sign/"+v.transit, body, &out, true); err != nil {
		return "", err
	}
	if out.Data.Signature == "" {
		return "", fmt.Errorf("transit returned an empty signature")
	}
	return out.Data.Signature, nil
}

// Verify checks a transit signature locally. No network, no Vault, no failure
// mode that a sealed Vault can cause.
func (v *vault) Verify(msg []byte, signature string) bool {
	version, raw, ok := parseSignature(signature)
	if !ok {
		return false
	}
	v.mu.RLock()
	key, known := v.pubKeys[version]
	v.mu.RUnlock()
	if !known {
		return false
	}
	return ed25519.Verify(key, msg, raw)
}

// parseSignature splits "vault:v3:<base64>". The version matters: it selects
// which public key to check against, which is what makes rotation survivable.
func parseSignature(s string) (version int, sig []byte, ok bool) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 || parts[0] != "vault" || !strings.HasPrefix(parts[1], "v") {
		return 0, nil, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(parts[1], "v"))
	if err != nil || n < 1 {
		return 0, nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil || len(raw) != ed25519.SignatureSize {
		return 0, nil, false
	}
	return n, raw, true
}

// refreshKeys pulls every public key version. Verified against transit: the
// export endpoint works whether or not the key was created exportable, because
// only the public half is ever leaving.
func (v *vault) refreshKeys() error {
	var out struct {
		Data struct {
			Keys map[string]string `json:"keys"`
		} `json:"data"`
	}
	if err := v.call(http.MethodGet, "transit/export/public-key/"+v.transit, nil, &out, true); err != nil {
		return err
	}
	keys := map[int]ed25519.PublicKey{}
	for ver, b64 := range out.Data.Keys {
		n, err := strconv.Atoi(ver)
		if err != nil {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			slog.Warn("transit key version is not usable", "version", ver)
			continue
		}
		keys[n] = ed25519.PublicKey(raw)
	}
	if len(keys) == 0 {
		return fmt.Errorf("transit returned no usable ed25519 public keys")
	}
	v.mu.Lock()
	v.pubKeys = keys
	v.mu.Unlock()
	return v.saveCachedKeys(out.Data.Keys)
}

func (v *vault) cachePath() string { return filepath.Join(v.stateDir, vaultKeyCacheFile) }

func (v *vault) saveCachedKeys(keys map[string]string) error {
	if v.stateDir == "" {
		return nil
	}
	if err := os.MkdirAll(v.stateDir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	// Public keys, so the mode is about integrity rather than secrecy: nobody
	// else should be able to substitute one.
	return os.WriteFile(v.cachePath(), data, 0o600)
}

// loadCachedKeys is what lets the relay verify agents before it has ever
// reached Vault -- after a reboot that brought both back at once, for instance.
func (v *vault) loadCachedKeys() error {
	if v.stateDir == "" {
		return fmt.Errorf("no state directory")
	}
	data, err := os.ReadFile(v.cachePath())
	if err != nil {
		return err
	}
	var keys map[string]string
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	loaded := map[int]ed25519.PublicKey{}
	for ver, b64 := range keys {
		n, err := strconv.Atoi(ver)
		if err != nil {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			continue
		}
		loaded[n] = ed25519.PublicKey(raw)
	}
	if len(loaded) == 0 {
		return fmt.Errorf("cache held no usable keys")
	}
	v.mu.Lock()
	v.pubKeys = loaded
	v.mu.Unlock()
	slog.Info("loaded cached transit keys", "versions", len(loaded))
	return nil
}

// call performs one Vault request. On 403 with a token it logs in again and
// retries once, which covers the AppRole token reaching the end of its TTL.
func (v *vault) call(method, path string, body, out any, authed bool) error {
	do := func() (*http.Response, error) {
		var rdr io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			rdr = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, v.addr+"/v1/"+path, rdr)
		if err != nil {
			return nil, err
		}
		if authed {
			v.mu.RLock()
			tok := v.token
			v.mu.RUnlock()
			req.Header.Set("X-Vault-Token", tok)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return v.httpc.Do(req)
	}

	resp, err := do()
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusForbidden && authed {
		resp.Body.Close()
		if err := v.login(); err != nil {
			return fmt.Errorf("vault rejected the token and re-login failed: %w", err)
		}
		if resp, err = do(); err != nil {
			return err
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("vault %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
