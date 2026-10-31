package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, builds with CGO_ENABLED=0

	"kandev-plugin-mindmap/server/contract"
)

// Store is the plugin's SQLite store plus the in-memory materialised state of
// every map that has been touched since start.
//
// Concurrency: one mutex (mu) serialises every write transaction and every
// read or write of cached map state. SQLite has a single writer anyway, and
// the plugin is local, so one lock keeps the engine simple and race-free.
// Read-only queries that do not touch cached state (op log, runs, tokens) run
// without the lock.
type Store struct {
	db      *sql.DB
	dataDir string

	mu   sync.Mutex
	maps map[string]*mapState

	// Now is the clock. Tests may replace it before use.
	Now func() time.Time
}

// Open opens (and migrates) $dataDir/mindmap.db and creates the assets dir.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dataDir, "assets"), 0o755); err != nil {
		return nil, err
	}
	dsn := filepath.Join(dataDir, "mindmap.db") +
		"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, dataDir: dataDir, maps: map[string]*mapState{}, Now: func() time.Time { return time.Now().UTC() }}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DataDir is the directory holding mindmap.db and assets/.
func (s *Store) DataDir() string { return s.dataDir }

func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v >= SchemaVersion {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if v < 1 {
		if _, err := tx.Exec(SchemaV1); err != nil {
			return fmt.Errorf("migration v1: %w", err)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) now() time.Time { return s.Now().UTC().Truncate(time.Millisecond) }

// --- kv ------------------------------------------------------------------------

// GetKV reads a plugin-wide setting. ok is false when the key is unset.
func (s *Store) GetKV(key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow("SELECT value FROM kv WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

// SetKV upserts a plugin-wide setting.
func (s *Store) SetKV(key, value string) error {
	_, err := s.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

// --- ids, times, json ------------------------------------------------------

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// NewID mints `<prefix>_<26 chars>`: 9 chars of millisecond time (so ids sort
// roughly by creation) and 17 random base-62 chars.
func NewID(prefix string) string {
	var b strings.Builder
	b.Grow(len(prefix) + 27)
	b.WriteString(prefix)
	b.WriteByte('_')
	ms := big.NewInt(time.Now().UnixMilli())
	ts := make([]byte, 9)
	sixty2 := big.NewInt(62)
	mod := new(big.Int)
	for i := 8; i >= 0; i-- {
		ms.DivMod(ms, sixty2, mod)
		ts[i] = base62[mod.Int64()]
	}
	b.Write(ts)
	rnd := make([]byte, 17)
	_, _ = rand.Read(rnd)
	for _, c := range rnd {
		b.WriteByte(base62[int(c)%62])
	}
	return b.String()
}

// hashID derives a stable id suffix from a seed (used for undo batch ids).
func hashID(prefix, marker, seed string) string {
	sum := sha256.Sum256([]byte(seed))
	n := new(big.Int).SetBytes(sum[:])
	out := make([]byte, 0, 26)
	sixty2 := big.NewInt(62)
	mod := new(big.Int)
	for len(out) < 26 {
		n.DivMod(n, sixty2, mod)
		out = append(out, base62[mod.Int64()])
	}
	return prefix + "_" + marker + string(out)
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return fmtTime(t)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// hashToken returns the hex sha256 of a raw token.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func errNotFound(format string, a ...any) error { return contract.Errf(contract.ErrNotFound, format, a...) }
