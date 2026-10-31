package store

import (
	"database/sql"
	"errors"
	"strings"
	"unicode"
)

// NodeMap returns the map a node belongs to.
func (s *Store) NodeMap(nodeID string) (string, error) {
	var m string
	err := s.db.QueryRow(`SELECT map_id FROM nodes WHERE id = ?`, nodeID).Scan(&m)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errNotFound("node %s not found", nodeID)
	}
	return m, err
}

// WorkspaceMaps lists the live map ids of a workspace.
func (s *Store) WorkspaceMaps(workspaceID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM maps WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY updated_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SearchHit is one full-text match.
type SearchHit struct {
	MapID, NodeID string
}

// ftsQuery turns free text into a safe FTS5 query: every word becomes a
// quoted prefix term, all required.
func ftsQuery(q string) string {
	words := strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	terms := make([]string, 0, len(words))
	for _, w := range words {
		if len(terms) == 12 {
			break
		}
		terms = append(terms, `"`+strings.ToLower(w)+`"*`)
	}
	return strings.Join(terms, " ")
}

// SearchText ranks live nodes of the given maps by full-text relevance.
func (s *Store) SearchText(mapIDs []string, query string, limit int) ([]SearchHit, error) {
	fq := ftsQuery(query)
	if fq == "" || len(mapIDs) == 0 {
		return nil, nil
	}
	args := []any{fq}
	for _, m := range mapIDs {
		args = append(args, m)
	}
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT n.map_id, n.id FROM nodes_fts f JOIN nodes n ON n.rowid = f.rowid
		WHERE nodes_fts MATCH ? AND n.deleted = 0 AND n.map_id IN (?`+strings.Repeat(",?", len(mapIDs)-1)+`)
		ORDER BY bm25(nodes_fts, 10.0, 3.0, 5.0, 2.0) LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.MapID, &h.NodeID); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
