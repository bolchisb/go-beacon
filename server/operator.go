package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// The operator account behind the dashboard: one username, one password, and
// the key that signs session cookies.
//
// Vault is the source of truth, and the record is cached on disk beside the
// transit keys. The cache is what keeps a sealed Vault from locking an operator
// out of the dashboard -- the same coupling the agent design exists to avoid,
// and the cached material is a password hash, so putting it on disk costs
// nothing.
//
// The admin token stays valid as a bearer credential throughout. It is what
// bootstraps the first account and what gets you back in if both Vault and the
// cache are gone.

const (
	operatorVaultPath = "beacon/operator"
	operatorCacheFile = "operator.json"

	// argon2id, deliberately on the slow side: a dashboard login happens
	// rarely, and this is the only thing standing between a guessed password
	// and command execution on every connected machine.
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

type operator struct {
	Username   string    `json:"username"`
	PasswordPH string    `json:"password_hash"`
	SessionKey string    `json:"session_key"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type operatorStore struct {
	vault    *vault
	stateDir string

	mu  sync.RWMutex
	rec *operator
}

func newOperatorStore(v *vault, stateDir string) *operatorStore {
	return &operatorStore{vault: v, stateDir: stateDir}
}

// load prefers Vault and falls back to the cache, so a relay that starts while
// Vault is sealed still lets its operator in.
func (s *operatorStore) load() {
	if rec, err := s.readVault(); err == nil {
		s.set(rec)
		if err := s.writeCache(rec); err != nil {
			slog.Warn("could not cache the operator record", "err", err)
		}
		slog.Info("operator account loaded", "user", rec.Username)
		return
	} else if !errors.Is(err, errNoOperator) {
		slog.Warn("could not read the operator account from Vault", "err", err)
	}

	rec, err := s.readCache()
	if err != nil {
		slog.Info("no operator account yet: the dashboard will ask for the admin token and set one up")
		return
	}
	s.set(rec)
	slog.Info("operator account loaded from cache", "user", rec.Username)
}

func (s *operatorStore) set(rec *operator) {
	s.mu.Lock()
	s.rec = rec
	s.mu.Unlock()
}

func (s *operatorStore) current() *operator {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rec
}

func (s *operatorStore) exists() bool { return s.current() != nil }

// verify checks a username and password against the stored record in constant
// time with respect to the password.
func (s *operatorStore) verify(username, password string) bool {
	rec := s.current()
	if rec == nil {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(username)), []byte(rec.Username)) != 1 {
		// Still spend the hashing time, so a wrong username is not measurably
		// faster to reject than a wrong password.
		_, _ = verifyPassword(rec.PasswordPH, password)
		return false
	}
	ok, err := verifyPassword(rec.PasswordPH, password)
	if err != nil {
		slog.Error("stored password hash is unusable", "err", err)
		return false
	}
	return ok
}

// save writes a new username and password. Vault is authoritative, so a failure
// there is a failure overall: a password that appears to change but does not
// survive a restart is worse than one that refuses to change.
func (s *operatorStore) save(username, password string, rotateSessions bool) error {
	username = strings.TrimSpace(username)
	if err := validateCredentials(username, password); err != nil {
		return err
	}

	ph, err := hashPassword(password)
	if err != nil {
		return err
	}

	sessionKey := ""
	if cur := s.current(); cur != nil && !rotateSessions {
		sessionKey = cur.SessionKey
	}
	if sessionKey == "" {
		sessionKey, err = randomKey()
		if err != nil {
			return err
		}
	}

	rec := &operator{
		Username:   username,
		PasswordPH: ph,
		SessionKey: sessionKey,
		UpdatedAt:  time.Now().UTC(),
	}
	if err := s.writeVault(rec); err != nil {
		return fmt.Errorf("saving to Vault: %w", err)
	}
	if err := s.writeCache(rec); err != nil {
		slog.Warn("saved to Vault but could not update the cache", "err", err)
	}
	s.set(rec)
	return nil
}

func validateCredentials(username, password string) error {
	if username == "" {
		return errors.New("a username is required")
	}
	if len(username) > 64 {
		return errors.New("that username is too long")
	}
	// Long rather than complex: length is what actually resists guessing, and
	// composition rules mostly produce passwords people write down.
	if len([]rune(password)) < 12 {
		return errors.New("the password must be at least 12 characters")
	}
	if len(password) > 1024 {
		return errors.New("that password is too long")
	}
	return nil
}

// ---- Vault -----------------------------------------------------------------

var errNoOperator = errors.New("no operator account stored")

func (s *operatorStore) readVault() (*operator, error) {
	if s.vault == nil || !s.vault.configured() {
		return nil, errNoOperator
	}
	var rec operator
	found, err := s.vault.kvGet(operatorVaultPath, &rec)
	if err != nil {
		return nil, err
	}
	if !found || rec.Username == "" {
		return nil, errNoOperator
	}
	return &rec, nil
}

func (s *operatorStore) writeVault(rec *operator) error {
	if s.vault == nil || !s.vault.configured() {
		return errors.New("no Vault configured, so there is nowhere durable to save this")
	}
	return s.vault.kvPut(operatorVaultPath, rec)
}

// ---- cache -----------------------------------------------------------------

func (s *operatorStore) cachePath() string {
	return filepath.Join(s.stateDir, operatorCacheFile)
}

func (s *operatorStore) writeCache(rec *operator) error {
	if s.stateDir == "" {
		return nil
	}
	if err := os.MkdirAll(s.stateDir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	// The password is hashed, but the session key is not: anyone holding it can
	// forge a cookie.
	return os.WriteFile(s.cachePath(), data, 0o600)
}

func (s *operatorStore) readCache() (*operator, error) {
	if s.stateDir == "" {
		return nil, errNoOperator
	}
	data, err := os.ReadFile(s.cachePath())
	if err != nil {
		return nil, err
	}
	var rec operator
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	if rec.Username == "" || rec.PasswordPH == "" {
		return nil, errNoOperator
	}
	return &rec, nil
}

// ---- password hashing ------------------------------------------------------

func hashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// verifyPassword reads the parameters back out of the stored hash rather than
// assuming the current constants, so raising the cost later does not lock
// anyone out of an account hashed under the old settings.
func verifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, fmt.Errorf("unrecognised password hash format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, err
	}
	var memory uint32
	var t uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &t, &threads); err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, t, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func randomKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(b), nil
}
