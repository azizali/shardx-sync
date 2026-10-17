package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/azizali/shardx-sync/internal/core"
	"github.com/zalando/go-keyring"
)

const keyringService = "ShardX Sync"
const keyringUser = "team"

// secretData is kept in the OS keychain (macOS Keychain, Windows Credential Manager, Linux Secret Service).
type secretData struct {
	Passphrase string        `json:"passphrase,omitempty"`
	Session    *core.Session `json:"session,omitempty"`
}

// secretStore falls back to a private file when no OS keychain is available (e.g. a bare Linux desktop).
type secretStore struct {
	mu       sync.Mutex
	fallback string
}

func newSecretStore(workDir string) *secretStore {
	return &secretStore{fallback: filepath.Join(workDir, "secrets.json")}
}

func (s *secretStore) load() secretData {
	var d secretData
	if v, err := keyring.Get(keyringService, keyringUser); err == nil {
		_ = json.Unmarshal([]byte(v), &d)
		return d
	}
	if b, err := os.ReadFile(s.fallback); err == nil {
		_ = json.Unmarshal(b, &d)
	}
	return d
}

func (s *secretStore) save(d secretData) {
	b, _ := json.Marshal(d)
	if err := keyring.Set(keyringService, keyringUser, string(b)); err == nil {
		_ = os.Remove(s.fallback)
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.fallback), 0o700)
	_ = os.WriteFile(s.fallback, b, 0o600)
}

func (s *secretStore) update(fn func(*secretData)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.load()
	fn(&d)
	s.save(d)
}

func (s *secretStore) Passphrase() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load().Passphrase
}

// LoadSession / SaveSession implement core.SessionStore.
func (s *secretStore) LoadSession() *core.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load().Session
}

func (s *secretStore) SaveSession(sess *core.Session) {
	s.update(func(d *secretData) { d.Session = sess })
}
