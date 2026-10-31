package share

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecretsFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "share-secrets.json")
	if err := os.WriteFile(path, []byte(`{"old":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// An existing temp file keeps its mode across WriteFile. Rename would
	// then leave share-secrets.json world-readable.
	if err := os.WriteFile(path+".tmp", []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("sl_1", "mms1.exampletoken"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	s2, err := OpenSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := s2.Get("sl_1"); !ok || got != "mms1.exampletoken" {
		t.Fatalf("reload %q %v", got, ok)
	}
}
