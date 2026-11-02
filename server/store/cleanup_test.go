package store

import (
	"testing"

	"kandev-plugin-mindmap/server/contract"
)

// The three cleanup behaviors: archive (settings flag + list split), delete
// (soft), and the refusals while agents or a share are live.

func settingsOp(mapID string, st contract.MapSettings) contract.Op {
	return contract.Op{ID: NewID(contract.PrefixOp), MapID: mapID, Type: contract.OpMapUpdate,
		Payload: &contract.MapUpdatePayload{Settings: &st}}
}

func TestArchiveSplitsMapListAndUndoRestores(t *testing.T) {
	s := openTest(t)
	m, host := newMap(t, s)
	m2, _ := newMap(t, s)

	// Host archives m by rewriting settings whole.
	st := contract.MapSettings{Archived: true, Layout: map[string]contract.Point{m.RootID: {X: -40, Y: 12}}}
	if _, err := s.Submit(m.ID, []contract.Op{settingsOp(m.ID, st)}, SubmitOptions{Actor: contract.Actor{ParticipantID: host.ID}}); err != nil {
		t.Fatal(err)
	}

	live, err := s.ListMaps("ws-1", "", false)
	if err != nil || len(live) != 1 || live[0].ID != m2.ID {
		t.Fatalf("live half %+v err=%v", live, err)
	}
	archived, err := s.ListMaps("ws-1", "", true)
	if err != nil || len(archived) != 1 || archived[0].ID != m.ID || !archived[0].Archived {
		t.Fatalf("archived half %+v err=%v", archived, err)
	}
	// A share-scoped query still sees the archived map.
	scoped, err := s.ListMaps("", m.ID, true)
	if err != nil || len(scoped) != 1 || !scoped[0].Archived {
		t.Fatalf("scoped archived query %+v err=%v", scoped, err)
	}
	// Settings landed on the map.
	mm, err := s.GetMap(m.ID)
	if err != nil || !mm.Settings.Archived || mm.Settings.Layout[m.RootID].X != -40 {
		t.Fatalf("settings after archive: %+v err=%v", mm, err)
	}

	// Undo puts it back in the live half (inverse captured the prior settings).
	if _, err := s.Undo(m.ID, host.ID, SubmitOptions{Actor: contract.Actor{ParticipantID: host.ID}}); err != nil {
		t.Fatal(err)
	}
	live, _ = s.ListMaps("ws-1", "", false)
	archived, _ = s.ListMaps("ws-1", "", true)
	if len(live) != 2 || len(archived) != 0 {
		t.Fatalf("after undo live=%d archived=%d", len(live), len(archived))
	}
}

func TestDeleteMapIsSoft(t *testing.T) {
	s := openTest(t)
	m, _ := newMap(t, s)
	if err := s.DeleteMap(m.ID); err != nil {
		t.Fatal(err)
	}
	live, err := s.ListMaps("ws-1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := s.ListMaps("ws-1", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 || len(archived) != 0 {
		t.Fatalf("deleted map still listed: live=%d archived=%d", len(live), len(archived))
	}
	if _, err := s.Snapshot(m.ID); code(err) != contract.ErrNotFound {
		t.Fatalf("snapshot of deleted map: %v", err)
	}
	// The row itself survives (soft delete, 30-day purge window).
	var deletedAt string
	if err := s.db.QueryRow(`SELECT deleted_at FROM maps WHERE id = ?`, m.ID).Scan(&deletedAt); err != nil || deletedAt == "" {
		t.Fatalf("maps row missing or not tombstoned: %q err=%v", deletedAt, err)
	}
	// Deleting again is a no-op, not an error.
	if err := s.DeleteMap(m.ID); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestDeleteMapRefusedWhileAgentsRun(t *testing.T) {
	s := openTest(t)
	m, _ := newMap(t, s)
	if _, err := s.db.Exec(`INSERT INTO runs (id, map_id, node_id, task_id, workspace_id, profile_id, harness, template, mode, budget_json, status, transport, participant_id, started_by, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		NewID("run"), m.ID, m.RootID, "task-1", "ws-1", "p", "claude", "research", "apply", "{}", "running", "mcp", "pt_x", "pt_y", s.now(), s.now()); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMap(m.ID); code(err) != contract.ErrConflict {
		t.Fatalf("want conflict while running, got %v", err)
	}
	if live, _ := s.ListMaps("ws-1", "", false); len(live) != 1 {
		t.Fatalf("refused delete still removed the map")
	}
}

func TestDeleteMapRefusedWhileShared(t *testing.T) {
	s := openTest(t)
	m, host := newMap(t, s)
	if err := s.InsertShare(NewID("shr"), m.ID, contract.ShareCloudflared, host.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMap(m.ID); code(err) != contract.ErrConflict {
		t.Fatalf("want conflict while shared, got %v", err)
	}
}

func TestLayoutSettingsValidation(t *testing.T) {
	s := openTest(t)
	m, host := newMap(t, s)
	asHost := SubmitOptions{Actor: contract.Actor{ParticipantID: host.ID}}
	cases := map[string]contract.MapSettings{
		"unknown node id": {Layout: map[string]contract.Point{"map_notanode00": {X: 1, Y: 1}}},
		"out of range":    {Layout: map[string]contract.Point{m.RootID: {X: 1e9, Y: 0}}},
	}
	for name, st := range cases {
		if _, err := s.Submit(m.ID, []contract.Op{settingsOp(m.ID, st)}, asHost); code(err) != contract.ErrInvalidOp {
			t.Errorf("layout %s: want invalid_op, got %v", name, err)
		} else if f := contract.AsError(err).Field; f != "settings.layout" && f != "ops[0].settings.layout" {
			t.Errorf("layout %s: field %q", name, contract.AsError(err).Field)
		}
	}
	// Guests cannot move nodes: settings stay host-only.
	guest, err := s.CreateGuest(m.ID, "G", "#00ff00")
	if err != nil {
		t.Fatal(err)
	}
	pos := contract.MapSettings{Layout: map[string]contract.Point{m.RootID: {X: 1, Y: 1}}}
	if _, err := s.Submit(m.ID, []contract.Op{settingsOp(m.ID, pos)}, SubmitOptions{Actor: contract.Actor{ParticipantID: guest.ID}}); code(err) != contract.ErrForbidden {
		t.Fatalf("guest layout: %v", err)
	}
}
