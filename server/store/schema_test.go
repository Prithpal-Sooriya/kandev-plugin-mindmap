package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSchemaV1Applies(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "mindmap.db")+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(SchemaV1); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := tx.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	want := []string{"maps", "nodes", "nodes_fts", "links", "ops", "participants", "runs", "run_refs",
		"patch_blocks", "proposals", "proposal_items", "assets", "share_links", "tokens", "activity", "kv"}
	for _, tbl := range want {
		var n int
		if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = ?", tbl).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing (err=%v)", tbl, err)
		}
	}
	// Smoke: a map, a root node, an op, FTS insert/delete, and the task_id uniqueness rule.
	exec := func(q string, args ...any) error { _, err := db.Exec(q, args...); return err }
	ts := "2026-10-01T12:00:00Z"
	if err := exec(`INSERT INTO maps (id, workspace_id, title, root_id, created_by, created_at, updated_at) VALUES ('map_a000000001','ws','T','nd_r000000001','pt_h000000001',?,?)`, ts, ts); err != nil {
		t.Fatal(err)
	}
	if err := exec(`INSERT INTO nodes (id, map_id, ord, title, kind, author_participant_id, rev, created_at, updated_at) VALUES ('nd_r000000001','map_a000000001','V','Root','topic','pt_h000000001',1,?,?)`, ts, ts); err != nil {
		t.Fatal(err)
	}
	if err := exec(`INSERT INTO nodes (id, map_id, ord, title, kind, author_participant_id, rev, created_at, updated_at) VALUES ('nd_x000000001','map_a000000001','V','X','bogus','pt_h000000001',1,?,?)`, ts, ts); err == nil {
		t.Error("kind CHECK not enforced")
	}
	if err := exec(`INSERT INTO ops (map_id, rev, id, type, payload_json, inverse_json, actor_participant_id, ts) VALUES ('map_a000000001',1,'op_a000000001','node.create','{}','{}','pt_h000000001',?)`, ts); err != nil {
		t.Fatal(err)
	}
	if err := exec(`INSERT INTO ops (map_id, rev, id, type, payload_json, inverse_json, actor_participant_id, ts) VALUES ('map_a000000001',2,'op_a000000001','node.create','{}','{}','pt_h000000001',?)`, ts); err == nil {
		t.Error("duplicate op id accepted")
	}
	if err := exec(`INSERT INTO nodes_fts (rowid, title, body_md, tags, source_titles) VALUES (1, 'Pricing model', 'freemium tiers', 'pricing', '')`); err != nil {
		t.Fatal(err)
	}
	var hits int
	if err := db.QueryRow(`SELECT count(*) FROM nodes_fts WHERE nodes_fts MATCH 'tier'`).Scan(&hits); err != nil || hits != 1 {
		t.Errorf("fts porter match: hits=%d err=%v", hits, err)
	}
	if err := exec(`DELETE FROM nodes_fts WHERE rowid = 1`); err != nil {
		t.Errorf("contentless delete: %v", err)
	}
	run := `INSERT INTO runs (id, map_id, node_id, task_id, workspace_id, profile_id, harness, template, mode, budget_json, status, transport, participant_id, started_by, created_at, updated_at)
		VALUES (?, 'map_a000000001', 'nd_r000000001', 'task-1', 'ws', 'p', 'claude', 'research', 'apply', '{}', 'queued', 'mcp', 'pt_g000000001', 'pt_h000000001', ?, ?)`
	if err := exec(run, "run_a000000001", ts, ts); err != nil {
		t.Fatal(err)
	}
	if err := exec(run, "run_b000000001", ts, ts); err == nil {
		t.Error("two runs share a task_id")
	}
}
