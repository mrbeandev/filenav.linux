package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// State is persisted as a single small JSON file. No database needed.
type State struct {
	Version      int                 `json:"version"`
	Secret       []byte              `json:"secret"` // HMAC key for share-access cookies
	PasscodeHash string              `json:"passcode_hash"`
	PasskeyOnly  bool                `json:"passkey_only"`
	UserHandle   []byte              `json:"user_handle"` // WebAuthn user.id
	Sessions     map[string]*Session `json:"sessions"`    // key: sha256(token) hex
	Passkeys     []*Passkey          `json:"passkeys"`
	Shares       map[string]*Share   `json:"shares"`
	Media        MediaSettings       `json:"media"`
}

type Session struct {
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	IP      string    `json:"ip"`
	UA      string    `json:"ua"`
	Method  string    `json:"method"` // passcode | passkey
}

type Passkey struct {
	ID        []byte    `json:"id"`
	Name      string    `json:"name"`
	PublicKey []byte    `json:"public_key"` // COSE_Key bytes
	Alg       int       `json:"alg"`
	SignCount uint32    `json:"sign_count"`
	Created   time.Time `json:"created"`
	LastUsed  time.Time `json:"last_used"`
}

type Share struct {
	ID          string    `json:"id"`
	Path        string    `json:"path"` // relative to root, "" = root
	IsDir       bool      `json:"is_dir"`
	Created     time.Time `json:"created"`
	Expires     time.Time `json:"expires"` // zero = never
	PassHash    string    `json:"pass_hash,omitempty"`
	AllowUpload bool      `json:"allow_upload"`
	UploadOnly  bool      `json:"upload_only"` // "drop box": visitors can upload but not list/download
	Note        string    `json:"note,omitempty"`
	Views       int64     `json:"views"`
	Downloads   int64     `json:"downloads"`
	MaxDownload int64     `json:"max_downloads,omitempty"` // 0 = unlimited
}

func (s *Share) Expired(now time.Time) bool {
	return !s.Expires.IsZero() && now.After(s.Expires)
}

type Store struct {
	mu    sync.RWMutex
	path  string
	state State
}

func OpenStore(path string) (*Store, error) {
	st := &Store{path: path}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &st.state); err != nil {
			return nil, err
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, err
	}
	s := &st.state
	changed := false
	if s.Version == 0 {
		s.Version = 1
		changed = true
	}
	if len(s.Secret) == 0 {
		s.Secret = randomBytes(32)
		changed = true
	}
	if len(s.UserHandle) == 0 {
		s.UserHandle = randomBytes(16)
		changed = true
	}
	if s.Sessions == nil {
		s.Sessions = map[string]*Session{}
	}
	if s.Shares == nil {
		s.Shares = map[string]*Share{}
	}
	if changed {
		if err := st.saveLocked(); err != nil {
			return nil, err
		}
	}
	return st, nil
}

func (st *Store) View(fn func(s *State)) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	fn(&st.state)
}

// Update runs fn under the write lock and persists the state if fn returns nil.
func (st *Store) Update(fn func(s *State) error) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := fn(&st.state); err != nil {
		return err
	}
	return st.saveLocked()
}

func (st *Store) saveLocked() error {
	b, err := json.MarshalIndent(&st.state, "", " ")
	if err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, st.path); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(st.path)); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// randomToken returns a URL-safe random string encoding n random bytes.
func randomToken(n int) string {
	return base64.RawURLEncoding.EncodeToString(randomBytes(n))
}
