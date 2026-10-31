package share

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// secretsFile is the host's copy of raw share tokens. The tokens table stores
// only sha256 hashes; this file exists so a restarted quick tunnel (new
// hostname) can rebuild https://<host>/m/<token>/. Guest, local, run and
// personal tokens are never written here. Mode 0600, same directory as the
// sqlite file.
const secretsName = "share-secrets.json"

// Secrets remembers raw share tokens for the life of the plugin and across
// restarts of this process.
type Secrets struct {
	path string
	mu   sync.Mutex
	toks map[string]string // share link id -> raw mms token
}

// OpenSecrets loads the file, or starts empty when it is missing.
func OpenSecrets(dir string) (*Secrets, error) {
	s := &Secrets{path: filepath.Join(dir, secretsName), toks: map[string]string{}}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s.toks); err != nil {
		return nil, err
	}
	if s.toks == nil {
		s.toks = map[string]string{}
	}
	return s, nil
}

// Put stores a raw share token and rewrites the file.
func (s *Secrets) Put(id, raw string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toks[id] = raw
	return s.flush()
}

// Get returns a raw share token.
func (s *Secrets) Get(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.toks[id]
	return v, ok && v != ""
}

// Delete removes one token and rewrites the file.
func (s *Secrets) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.toks, id)
	return s.flush()
}

func (s *Secrets) flush() error {
	if len(s.toks) == 0 {
		err := os.Remove(s.path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	b, err := json.Marshal(s.toks)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
