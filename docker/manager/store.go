package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"sync"
)

// J is a JSON object. The secrets keep the phone app's layout, so a backup
// or a move works both ways between the phone and Docker:
//
//	bundle      provisioner bundle (slot, tokens, bot, owner, ...)
//	settings    { prov_secret, pickle_key, api_secret, relay_port, wacall_port,
//	              extra_users[], options{}, calls_enabled }
//	session     { homeserver, user_id, access_token, device_id }
//	gateway_url e.g. https://wap.tjena.eu
type J = map[string]any

// Store is a JSON file readable only by this user (0600).
type Store struct {
	mu   sync.Mutex
	path string
}

func (s *Store) Load() (J, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *Store) load() (J, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return J{}, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeJ(data)
}

func decodeJ(data []byte) (J, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var m J
	if err := d.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		m = J{}
	}
	return m, nil
}

func (s *Store) Save(m J) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(m)
}

func (s *Store) save(m J) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, data)
}

func (s *Store) Update(fn func(J) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return err
	}
	if err = fn(m); err != nil {
		return err
	}
	return s.save(m)
}

func (s *Store) Wipe() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = os.Remove(s.path)
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// obj returns m[k] as an object, creating it if missing.
func obj(m J, k string) J {
	if v, ok := m[k].(map[string]any); ok {
		return v
	}
	v := J{}
	m[k] = v
	return v
}

// get returns m[k] as an object, or nil.
func get(m J, k string) J {
	v, _ := m[k].(map[string]any)
	return v
}

func str(m J, k string) string {
	if m == nil {
		return ""
	}
	v, _ := m[k].(string)
	return v
}

func num(m J, k string) int {
	if m == nil {
		return 0
	}
	switch v := m[k].(type) {
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func boolean(m J, k string) bool {
	if m == nil {
		return false
	}
	v, _ := m[k].(bool)
	return v
}

func randomSecret(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Prefs: the manager's own settings (not secret, not part of backups).
type Prefs struct {
	Enabled  bool   `json:"enabled"`   // bridge should run
	PassSalt string `json:"pass_salt"` // web UI password (PBKDF2), unless WEBUI_PASSWORD is set
	PassHash string `json:"pass_hash"`

	mu   sync.Mutex
	path string
}

func loadPrefs(path string) *Prefs {
	p := &Prefs{path: path}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, p)
	}
	return p
}

func (p *Prefs) Update(fn func(*Prefs)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(p)
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return writeFileAtomic(p.path, data)
}

func (p *Prefs) Get() (enabled bool, salt, hash string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Enabled, p.PassSalt, p.PassHash
}
