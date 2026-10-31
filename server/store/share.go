package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"kandev-plugin-mindmap/server/contract"
)

// ShareRow is a share_links row plus the columns the wire type does not carry.
type ShareRow struct {
	contract.ShareLink
	TokenID    string
	PublicHost string
	CreatedBy  string
}

// InsertShare inserts a share link in status starting. The share token is
// minted afterwards (the tokens row references this id).
func (s *Store) InsertShare(id, mapID string, provider contract.ShareProvider, createdBy string) error {
	_, err := s.db.Exec(`INSERT INTO share_links (id, map_id, provider, status, created_by, created_at)
		VALUES (?,?,?,?,?,?)`, id, mapID, string(provider), string(contract.ShareStarting), createdBy, fmtTime(s.now()))
	return err
}

// SetShareToken records which mms token belongs to a link.
func (s *Store) SetShareToken(id, tokenID string) error {
	_, err := s.db.Exec(`UPDATE share_links SET token_id = ? WHERE id = ?`, tokenID, id)
	return err
}

// ActivateShare marks a link active and stores the tunnel hostname (no secret).
func (s *Store) ActivateShare(id, publicHost string) error {
	_, err := s.db.Exec(`UPDATE share_links SET status = ?, public_host = ?, error = NULL, stopped_at = NULL WHERE id = ?`,
		string(contract.ShareActive), publicHost, id)
	return err
}

// FailShare marks a link error so the one-live-share index releases the map.
func (s *Store) FailShare(id, message string) error {
	_, err := s.db.Exec(`UPDATE share_links SET status = ?, error = ?, stopped_at = ? WHERE id = ?`,
		string(contract.ShareError), message, fmtTime(s.now()), id)
	return err
}

// StopShare marks a link stopped.
func (s *Store) StopShare(id string) error {
	_, err := s.db.Exec(`UPDATE share_links SET status = ?, stopped_at = ? WHERE id = ? AND status IN ('starting','active')`,
		string(contract.ShareStopped), fmtTime(s.now()), id)
	return err
}

// SetActiveHosts updates the hostname of every live share (a quick tunnel
// hostname changes when the process restarts).
func (s *Store) SetActiveHosts(publicHost string) error {
	_, err := s.db.Exec(`UPDATE share_links SET public_host = ?, status = ? WHERE status IN ('starting','active')`,
		publicHost, string(contract.ShareActive))
	return err
}

func scanShare(r scanner) (ShareRow, error) {
	var row ShareRow
	var host, tokenID, errText, stopped sql.NullString
	var created string
	err := r.Scan(&row.ID, &row.MapID, &row.Provider, &row.Status, &host, &tokenID, &errText, &row.CreatedBy, &created, &stopped)
	if err != nil {
		return row, err
	}
	row.PublicHost = host.String
	row.TokenID = tokenID.String
	row.Error = errText.String
	row.CreatedAt = parseTime(created)
	row.StoppedAt = parseTime(stopped.String)
	return row, nil
}

const shareCols = `id, map_id, provider, status, public_host, token_id, error, created_by, created_at, stopped_at`

// LiveShare returns the starting or active link for a map.
func (s *Store) LiveShare(mapID string) (ShareRow, bool, error) {
	row, err := scanShare(s.db.QueryRow(`SELECT `+shareCols+` FROM share_links WHERE map_id = ? AND status IN ('starting','active')`, mapID))
	if errors.Is(err, sql.ErrNoRows) {
		return ShareRow{}, false, nil
	}
	return row, err == nil, err
}

// ListLiveShares returns every starting or active link.
func (s *Store) ListLiveShares() ([]ShareRow, error) {
	rows, err := s.db.Query(`SELECT ` + shareCols + ` FROM share_links WHERE status IN ('starting','active') ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ShareRow{}
	for rows.Next() {
		row, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// CountLiveShares is how many maps are currently shared.
func (s *Store) CountLiveShares() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM share_links WHERE status IN ('starting','active')`).Scan(&n)
	return n, err
}

// RevokeShareLinkTokens revokes the share token and every guest token bound
// to it. Local, run and personal tokens are left alone.
func (s *Store) RevokeShareLinkTokens(shareLinkID string) error {
	_, err := s.db.Exec(`UPDATE tokens SET revoked_at = ? WHERE share_link_id = ? AND revoked_at IS NULL`, fmtTime(s.now()), shareLinkID)
	return err
}

// RevokeTokensByKind revokes every live token of the given kinds.
func (s *Store) RevokeTokensByKind(kinds ...contract.TokenKind) error {
	if len(kinds) == 0 {
		return nil
	}
	q := `UPDATE tokens SET revoked_at = ? WHERE revoked_at IS NULL AND kind IN (?` + repeatQ(len(kinds)-1) + `)`
	args := []any{fmtTime(s.now())}
	for _, k := range kinds {
		args = append(args, string(k))
	}
	_, err := s.db.Exec(q, args...)
	return err
}

func repeatQ(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += ",?"
	}
	return s
}

// TouchToken records use and extends an idle expiry (guest sessions).
func (s *Store) TouchToken(id string, ttl time.Duration) error {
	exp := s.now().Add(ttl)
	_, err := s.db.Exec(`UPDATE tokens SET last_used_at = ?, expires_at = ? WHERE id = ? AND revoked_at IS NULL`,
		fmtTime(s.now()), fmtTime(exp), id)
	return err
}

// CountActiveRuns counts non-terminal runs on a map (the concurrency cap).
// Swarm coordinators have no agent of their own and do not count.
func (s *Store) CountActiveRuns(mapID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM runs WHERE map_id = ? AND status IN ('queued','running','waiting','review')
		AND NOT (template = 'swarm' AND COALESCE(lens, '') = '')`, mapID).Scan(&n)
	return n, err
}

// ListActiveRuns returns every non-terminal run, oldest first. The kill
// switch stops these.
func (s *Store) ListActiveRuns() ([]RunRecord, error) {
	rows, err := s.db.Query(`SELECT ` + runCols + ` FROM runs WHERE status IN ('queued','running','waiting','review') ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunRecord{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Audit returns who started runs on a map, oldest first, up to limit.
func (s *Store) Audit(mapID string, limit int) ([]contract.ActivityEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT id, map_id, type, actor_participant_id, coalesce(actor_run_id,''), summary, node_ids_json,
		coalesce(count,0), coalesce(rev,0), ts FROM activity
		WHERE map_id = ? AND type IN (?, ?) ORDER BY ts ASC, id ASC LIMIT ?`,
		mapID, string(contract.ActivityRunStarted), string(contract.ActivityRunSpawned), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanActivity(rows)
}

func scanActivity(rows *sql.Rows) ([]contract.ActivityEvent, error) {
	out := []contract.ActivityEvent{}
	for rows.Next() {
		var ev contract.ActivityEvent
		var ids, ts string
		if err := rows.Scan(&ev.ID, &ev.MapID, &ev.Type, &ev.Actor.ParticipantID, &ev.Actor.RunID, &ev.Summary, &ids, &ev.Count, &ev.Rev, &ts); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(ids), &ev.NodeIDs)
		if len(ev.NodeIDs) == 0 {
			ev.NodeIDs = nil
		}
		ev.TS = parseTime(ts)
		out = append(out, ev)
	}
	return out, rows.Err()
}
