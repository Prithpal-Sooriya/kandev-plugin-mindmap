package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kandev-plugin-mindmap/server/contract"
)

// traversalSHA is a 64-character "hash" that blobPath once resolved to
// <dataDir>/secret.txt: assets/<"..">/<sha> climbs out of the assets folder.
func traversalSHA(t *testing.T, dataDir string) string {
	t.Helper()
	tail := "../" + filepath.Base(dataDir) + "/"
	pad := strings.Repeat("./", (64-len(tail)-len("secret.txt"))/2)
	sha := tail + pad + "secret.txt"
	if len(sha) < 64 {
		sha = strings.Replace(sha, "../", "..//", 1)
	}
	if len(sha) != 64 || sha[:2] != ".." {
		t.Fatalf("bad payload %q (%d)", sha, len(sha))
	}
	if got := filepath.Join(dataDir, "assets", sha[:2], sha); got != filepath.Join(dataDir, "secret.txt") {
		t.Fatalf("payload resolves to %s", got)
	}
	return sha
}

func TestAssetAttachRejectsPathTraversal(t *testing.T) {
	s := openTest(t)
	m, host := newMap(t, s)
	secret := filepath.Join(s.DataDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	sha := traversalSHA(t, s.DataDir())
	a := contract.Asset{ID: NewID(contract.PrefixAsset), NodeID: m.RootID, Kind: contract.AssetFile, MIME: "text/plain", Bytes: 10, SHA256: sha}
	op := contract.Op{ID: NewID(contract.PrefixOp), MapID: m.ID, Type: contract.OpAssetAttach, Payload: &contract.AssetAttachPayload{Asset: a}}
	if _, err := s.Submit(m.ID, []contract.Op{op}, SubmitOptions{Actor: contract.Actor{ParticipantID: host.ID}}); code(err) != contract.ErrInvalidOp {
		t.Fatalf("traversal attach: %v", err)
	}
	upper := strings.Repeat("A", 64)
	a.SHA256, a.ID = upper, NewID(contract.PrefixAsset)
	op = contract.Op{ID: NewID(contract.PrefixOp), MapID: m.ID, Type: contract.OpAssetAttach, Payload: &contract.AssetAttachPayload{Asset: a}}
	if _, err := s.Submit(m.ID, []contract.Op{op}, SubmitOptions{Actor: contract.Actor{ParticipantID: host.ID}}); code(err) != contract.ErrInvalidOp {
		t.Fatalf("uppercase hash attach: %v", err)
	}

	// A row stored before validation existed must not open the file either.
	a.SHA256, a.ID = sha, NewID(contract.PrefixAsset)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAsset(tx, m.ID, a, false); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, p, err := s.AssetFile(m.ID, a.ID); err == nil || p != "" {
		t.Fatalf("AssetFile served %q (%v)", p, err)
	}
	if p, err := s.BlobPath(sha); err == nil || p != "" {
		t.Fatalf("BlobPath accepted traversal: %q", p)
	}
	good := strings.Repeat("ab", 32)
	if p, err := s.BlobPath(good); err != nil || p != filepath.Join(s.DataDir(), "assets", "ab", good) {
		t.Fatalf("BlobPath(valid) = %q, %v", p, err)
	}
}

func TestMapSettingsAreHostOnly(t *testing.T) {
	s := openTest(t)
	m, host := newMap(t, s)
	guest, err := s.CreateGuest(m.ID, "Mallory", "#ff0000")
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := s.CreateRun(NewRun{MapID: m.ID, NodeID: m.RootID, StartedBy: host.ID}, 4242)
	if err != nil {
		t.Fatal(err)
	}
	upd := func(p *contract.MapUpdatePayload) contract.Op {
		return contract.Op{ID: NewID(contract.PrefixOp), MapID: m.ID, Type: contract.OpMapUpdate, Payload: p}
	}
	open := contract.MapSettings{RequireGuestRunApproval: false, MaxConcurrentRun: 1000, DefaultBudget: &contract.Budget{MaxNodes: 100000}}
	repo := "repo-evil"
	profile := contract.MapSettings{DefaultProfileID: "prof-expensive"}
	asGuest := SubmitOptions{Actor: contract.Actor{ParticipantID: guest.ID}}
	for name, p := range map[string]*contract.MapUpdatePayload{
		"settings":           {Settings: &open},
		"repo_id":            {RepoID: &repo},
		"default_profile_id": {Settings: &profile},
	} {
		if _, err := s.Submit(m.ID, []contract.Op{upd(p)}, asGuest); code(err) != contract.ErrForbidden {
			t.Errorf("guest map.update %s: %v", name, err)
		}
	}
	asAgent := SubmitOptions{Actor: contract.Actor{ParticipantID: run.ParticipantID, RunID: run.ID}}
	if _, err := s.Submit(m.ID, []contract.Op{upd(&contract.MapUpdatePayload{Settings: &open})}, asAgent); code(err) != contract.ErrForbidden {
		t.Errorf("agent map.update settings: %v", err)
	}
	// A guest may still rename the map.
	title := "Renamed by guest"
	if _, err := s.Submit(m.ID, []contract.Op{upd(&contract.MapUpdatePayload{Title: &title})}, asGuest); err != nil {
		t.Fatalf("guest rename: %v", err)
	}
	got, _ := s.GetMap(m.ID)
	if got.Title != title || got.Settings.MaxConcurrentRun != 0 || got.RepoID != "" || got.Settings.DefaultProfileID != "" {
		t.Fatalf("map after guest ops: %+v", got)
	}
	// The host still can.
	locked := contract.MapSettings{RequireGuestRunApproval: true, MaxConcurrentRun: 2}
	if _, err := s.Submit(m.ID, []contract.Op{upd(&contract.MapUpdatePayload{Settings: &locked, RepoID: &repo})},
		SubmitOptions{Actor: contract.Actor{ParticipantID: host.ID}}); err != nil {
		t.Fatalf("host settings: %v", err)
	}
	got, _ = s.GetMap(m.ID)
	if !got.Settings.RequireGuestRunApproval || got.Settings.MaxConcurrentRun != 2 || got.RepoID != repo {
		t.Fatalf("map after host update: %+v", got)
	}
	// A guest's undo of its rename restores the title only.
	if _, err := s.Undo(m.ID, guest.ID, asGuest); err != nil {
		t.Fatalf("guest undo of own rename: %v", err)
	}
	got, _ = s.GetMap(m.ID)
	if got.Title != "Pricing strategy" || !got.Settings.RequireGuestRunApproval || got.RepoID != repo {
		t.Fatalf("map after guest undo: %+v", got)
	}
}
