package store

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"kandev-plugin-mindmap/server/contract"
)

// --- participants ---------------------------------------------------------------

func insertParticipant(tx execer, p contract.Participant) error {
	_, err := tx.Exec(`INSERT INTO participants (id, map_id, kind, name, color, is_host, harness, run_id, joined_at, last_seen_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, p.ID, p.MapID, string(p.Kind), p.Name, p.Color, boolInt(p.IsHost), nullStr(string(p.Harness)),
		nullStr(p.RunID), fmtTime(p.JoinedAt), fmtTime(p.LastSeen))
	return err
}

const participantCols = `id, map_id, kind, name, color, is_host, coalesce(harness,''), coalesce(run_id,''), joined_at, last_seen_at`

func scanParticipant(r scanner) (contract.Participant, error) {
	var p contract.Participant
	var host int
	var joined, seen string
	err := r.Scan(&p.ID, &p.MapID, &p.Kind, &p.Name, &p.Color, &host, &p.Harness, &p.RunID, &joined, &seen)
	p.IsHost, p.JoinedAt, p.LastSeen = host == 1, parseTime(joined), parseTime(seen)
	return p, err
}

// ListParticipants lists everyone who has joined a map.
func (s *Store) ListParticipants(mapID string) ([]contract.Participant, error) {
	rows, err := s.db.Query(`SELECT `+participantCols+` FROM participants WHERE map_id = ? ORDER BY joined_at, id`, mapID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contract.Participant{}
	for rows.Next() {
		p, err := scanParticipant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetParticipant returns one participant.
func (s *Store) GetParticipant(id string) (contract.Participant, error) {
	p, err := scanParticipant(s.db.QueryRow(`SELECT `+participantCols+` FROM participants WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, errNotFound("participant %s not found", id)
	}
	return p, err
}

// HostParticipant returns the host participant of a map.
func (s *Store) HostParticipant(mapID string) (contract.Participant, error) {
	p, err := scanParticipant(s.db.QueryRow(`SELECT `+participantCols+` FROM participants WHERE map_id = ? AND is_host = 1 ORDER BY joined_at LIMIT 1`, mapID))
	if errors.Is(err, sql.ErrNoRows) {
		return p, errNotFound("map %s has no host", mapID)
	}
	return p, err
}

// CreateGuest adds a human (non-host) participant.
func (s *Store) CreateGuest(mapID, name, color string) (contract.Participant, error) {
	now := s.now()
	if name == "" {
		name = "Guest"
	}
	if len(name) > contract.MaxDisplayNameLen {
		name = name[:contract.MaxDisplayNameLen]
	}
	if contract.ValidateColor("color", color) != nil || color == "" {
		color = "#e8a33d"
	}
	p := contract.Participant{ID: NewID(contract.PrefixParticipant), MapID: mapID, Kind: contract.ParticipantHuman, Name: name, Color: color,
		JoinedAt: now, LastSeen: now}
	return p, insertParticipant(s.db, p)
}

// TouchParticipant updates last_seen_at.
func (s *Store) TouchParticipant(id string) error {
	_, err := s.db.Exec(`UPDATE participants SET last_seen_at = ? WHERE id = ?`, fmtTime(s.now()), id)
	return err
}

// --- runs ----------------------------------------------------------------------

// RunRecord is a run plus the plugin-private columns the contract does not
// carry.
type RunRecord struct {
	contract.Run
	WorkspaceID   string
	ReportStatus  contract.ReportStatus
	StopRequested bool
}

// DefaultBudget applies to top-level runs whose request and map leave a
// field at zero.
var DefaultBudget = contract.Budget{MaxDepth: 4, MaxNodes: 60, MaxSubagents: 4, MaxMinutes: 30}

// NewRun is the input of CreateRun.
type NewRun struct {
	MapID, NodeID, WorkspaceID string
	ProfileID, ProfileName     string
	Harness                    contract.Harness
	Template                   contract.Template
	Lens                       contract.Lens
	Mode                       contract.RunMode
	Goal                       string
	Budget                     *contract.Budget
	ParentRunID                string
	SwarmID                    string
	Transport                  contract.ToolTransport
	StartedBy                  string // human participant id
}

// CreateRun creates a queued run, its agent participant, its run token and,
// in propose mode, its open proposal. A child run (ParentRunID set) costs one
// sub-agent from the root run's pool and its budget is clamped to what the
// pool has left. Returns the run and the raw run token.
func (s *Store) CreateRun(nr NewRun, port int) (RunRecord, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.state(nr.MapID)
	if err != nil {
		return RunRecord{}, "", err
	}
	if _, err := st.liveNode("node_id", nr.NodeID); err != nil {
		return RunRecord{}, "", err
	}
	if nr.Template == "" {
		nr.Template = contract.TemplateResearch
	}
	if !nr.Template.Valid() {
		return RunRecord{}, "", contract.FieldErr(contract.ErrBadRequest, "template", "unknown template %q", nr.Template)
	}
	if nr.Mode == "" {
		nr.Mode = st.m.Settings.DefaultMode
	}
	if nr.Mode == "" {
		nr.Mode = nr.Template.DefaultMode()
	}
	if nr.Harness == "" || !nr.Harness.Valid() {
		nr.Harness = contract.HarnessOther
	}
	if nr.Transport == "" {
		nr.Transport = contract.TransportMCP
	}
	now := s.now()
	r := RunRecord{WorkspaceID: nr.WorkspaceID, Run: contract.Run{
		ID: NewID(contract.PrefixRun), MapID: nr.MapID, NodeID: nr.NodeID, ProfileID: nr.ProfileID, ProfileName: nr.ProfileName,
		Harness: nr.Harness, Template: nr.Template, Lens: nr.Lens, Mode: nr.Mode, Goal: nr.Goal, ParentRunID: nr.ParentRunID,
		SwarmID: nr.SwarmID, Status: contract.RunQueued, Transport: nr.Transport, StartedBy: nr.StartedBy, CreatedAt: now, UpdatedAt: now,
	}}
	if r.WorkspaceID == "" {
		r.WorkspaceID = st.m.WorkspaceID
	}
	r.RootRunID = r.ID
	if nr.ParentRunID != "" {
		parent, err := s.GetRun(nr.ParentRunID)
		if err != nil {
			return RunRecord{}, "", err
		}
		r.RootRunID = parent.RootRunID
		left, err := s.budgetLeftLocked(r.RootRunID)
		if err != nil {
			return RunRecord{}, "", err
		}
		if left.Subagents == 0 {
			return RunRecord{}, "", contract.Errf(contract.ErrBudgetExceeded, "no sub-agents left in this run's budget")
		}
		b := contract.Budget{}
		if nr.Budget != nil {
			b = *nr.Budget
		}
		r.Budget = clampBudget(b, left, parent.Budget)
	} else {
		r.Budget = resolveBudget(nr.Budget, st.m.Settings.DefaultBudget)
	}
	r.ParticipantID = NewID(contract.PrefixParticipant)
	name := string(r.Template)
	if r.Lens != "" {
		name = string(r.Lens)
	}
	if r.ProfileName != "" {
		name += " (" + r.ProfileName + ")"
	}
	agent := contract.Participant{ID: r.ParticipantID, MapID: r.MapID, Kind: contract.ParticipantAgent, Name: name,
		Color: agentColor(r.ID), Harness: r.Harness, RunID: r.ID, JoinedAt: now, LastSeen: now}
	raw, tokHash, err := newRawToken(contract.TokenRun, port)
	if err != nil {
		return RunRecord{}, "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return RunRecord{}, "", err
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertRun(tx, r); err != nil {
		return RunRecord{}, "", err
	}
	if err := insertParticipant(tx, agent); err != nil {
		return RunRecord{}, "", err
	}
	if _, err := tx.Exec(`INSERT INTO tokens (id, kind, hash, map_id, run_id, participant_id, created_at) VALUES (?,?,?,?,?,?,?)`,
		NewID("tk"), string(contract.TokenRun), tokHash, r.MapID, r.ID, r.ParticipantID, fmtTime(now)); err != nil {
		return RunRecord{}, "", err
	}
	if r.Mode == contract.ModePropose {
		r.ProposalID = NewID(contract.PrefixProposal)
		if _, err := tx.Exec(`INSERT INTO proposals (id, map_id, run_id, base_rev, status, author_participant_id, created_at) VALUES (?,?,?,?,?,?,?)`,
			r.ProposalID, r.MapID, r.ID, st.m.Rev, string(contract.ProposalOpen), r.ParticipantID, fmtTime(now)); err != nil {
			return RunRecord{}, "", err
		}
		if _, err := tx.Exec(`UPDATE runs SET proposal_id = ? WHERE id = ?`, r.ProposalID, r.ID); err != nil {
			return RunRecord{}, "", err
		}
	}
	if nr.ParentRunID != "" {
		if _, err := tx.Exec(`UPDATE runs SET usage_subagents = usage_subagents + 1 WHERE id = ?`, nr.ParentRunID); err != nil {
			return RunRecord{}, "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return RunRecord{}, "", err
	}
	return r, raw, nil
}

func agentColor(seed string) string {
	palette := []string{"#7c5cff", "#00a3a3", "#e0567a", "#3f9b4f", "#d9822b", "#2f7de1", "#b04fc7", "#8a6d3b"}
	h := 0
	for _, c := range seed {
		h = h*31 + int(c)
	}
	if h < 0 {
		h = -h
	}
	return palette[h%len(palette)]
}

func resolveBudget(req, mapDefault *contract.Budget) contract.Budget {
	b := DefaultBudget
	for _, src := range []*contract.Budget{mapDefault, req} {
		if src == nil {
			continue
		}
		if src.MaxDepth > 0 {
			b.MaxDepth = src.MaxDepth
		}
		if src.MaxNodes > 0 {
			b.MaxNodes = src.MaxNodes
		}
		if src.MaxSubagents > 0 {
			b.MaxSubagents = src.MaxSubagents
		}
		if src.MaxMinutes > 0 {
			b.MaxMinutes = src.MaxMinutes
		}
	}
	return b
}

// clampBudget gives a child the requested budget, defaulting to and never
// exceeding what the pool has left (sub-agents: the pool minus this child).
func clampBudget(req contract.Budget, left contract.BudgetLeft, parent contract.Budget) contract.Budget {
	clamp := func(want, max int) int {
		if max < 0 {
			return want
		}
		if want <= 0 || want > max {
			return max
		}
		return want
	}
	subs := left.Subagents
	if subs > 0 {
		subs--
	}
	return contract.Budget{
		MaxDepth:     clamp(req.MaxDepth, parent.MaxDepth),
		MaxNodes:     clamp(req.MaxNodes, left.Nodes),
		MaxSubagents: clamp(req.MaxSubagents, subs),
		MaxMinutes:   clamp(req.MaxMinutes, parent.MaxMinutes),
	}
}

func insertRun(tx execer, r RunRecord) error {
	_, err := tx.Exec(`INSERT INTO runs (id, map_id, node_id, task_id, session_id, workspace_id, profile_id, profile_name, harness, template, lens, mode,
		goal, budget_json, parent_run_id, root_run_id, swarm_id, status, transport, participant_id, started_by, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.MapID, r.NodeID, nullStr(r.TaskID), nullStr(r.SessionID), r.WorkspaceID, r.ProfileID, nullStr(r.ProfileName), string(r.Harness),
		string(r.Template), nullStr(string(r.Lens)), string(r.Mode), nullStr(r.Goal), mustJSON(r.Budget), nullStr(r.ParentRunID), r.RootRunID,
		nullStr(r.SwarmID), string(r.Status), string(r.Transport), r.ParticipantID, r.StartedBy, fmtTime(r.CreatedAt), fmtTime(r.UpdatedAt))
	return err
}

const runCols = `id, map_id, node_id, coalesce(task_id,''), coalesce(session_id,''), workspace_id, profile_id, coalesce(profile_name,''), harness,
	template, coalesce(lens,''), mode, coalesce(goal,''), budget_json, usage_nodes, usage_subagents, usage_depth, coalesce(parent_run_id,''),
	coalesce(root_run_id,''), coalesce(swarm_id,''), status, coalesce(waiting_reason,''), transport, participant_id, started_by,
	coalesce(proposal_id,''), coalesce(summary,''), coalesce(error,''), coalesce(last_output,''), reported, coalesce(report_status,''),
	stop_requested, created_at, updated_at, coalesce(ended_at,'')`

func scanRun(r scanner) (RunRecord, error) {
	var x RunRecord
	var budget, created, updated, ended string
	var reported, stop int
	err := r.Scan(&x.ID, &x.MapID, &x.NodeID, &x.TaskID, &x.SessionID, &x.WorkspaceID, &x.ProfileID, &x.ProfileName, &x.Harness,
		&x.Template, &x.Lens, &x.Mode, &x.Goal, &budget, &x.Usage.Nodes, &x.Usage.Subagents, &x.Usage.Depth, &x.ParentRunID,
		&x.RootRunID, &x.SwarmID, &x.Status, &x.WaitingReason, &x.Transport, &x.ParticipantID, &x.StartedBy,
		&x.ProposalID, &x.Summary, &x.Error, &x.LastOutput, &reported, &x.ReportStatus, &stop, &created, &updated, &ended)
	if err != nil {
		return x, err
	}
	_ = json.Unmarshal([]byte(budget), &x.Budget)
	x.Reported, x.StopRequested = reported == 1, stop == 1
	x.CreatedAt, x.UpdatedAt, x.EndedAt = parseTime(created), parseTime(updated), parseTime(ended)
	return x, nil
}

func (s *Store) getRunWhere(where string, arg any) (RunRecord, error) {
	r, err := scanRun(s.db.QueryRow(`SELECT `+runCols+` FROM runs WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return r, errNotFound("run not found")
	}
	return r, err
}

// GetRun returns a run by id.
func (s *Store) GetRun(id string) (RunRecord, error) { return s.getRunWhere("id = ?", id) }

// RunByTask is the task_id -> run access check for Kandev MCP tool calls.
func (s *Store) RunByTask(taskID string) (RunRecord, error) {
	if taskID == "" {
		return RunRecord{}, errNotFound("run not found")
	}
	return s.getRunWhere("task_id = ?", taskID)
}

// ListRuns lists a map's runs, newest first (at most 200).
func (s *Store) ListRuns(mapID string) ([]contract.Run, error) {
	rows, err := s.db.Query(`SELECT `+runCols+` FROM runs WHERE map_id = ? ORDER BY created_at DESC, id DESC LIMIT 200`, mapID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contract.Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r.Run)
	}
	return out, rows.Err()
}

// SetRunTask records the Kandev task (and session) of a run. It must be
// called before the agent can call a tool.
func (s *Store) SetRunTask(runID, taskID, sessionID string) error {
	_, err := s.db.Exec(`UPDATE runs SET task_id = ?, session_id = coalesce(?, session_id), updated_at = ? WHERE id = ?`,
		nullStr(taskID), nullStr(sessionID), fmtTime(s.now()), runID)
	return err
}

// SaveRun writes a run's mutable fields.
func (s *Store) SaveRun(r RunRecord) (RunRecord, error) {
	r.UpdatedAt = s.now()
	if r.Status.Terminal() && r.EndedAt.IsZero() {
		r.EndedAt = r.UpdatedAt
	}
	if !r.Status.Terminal() {
		r.EndedAt = time.Time{}
	}
	_, err := s.db.Exec(`UPDATE runs SET task_id = ?, session_id = ?, status = ?, waiting_reason = ?, summary = ?, error = ?, last_output = ?,
		reported = ?, report_status = ?, stop_requested = ?, usage_nodes = ?, usage_subagents = ?, usage_depth = ?, updated_at = ?, ended_at = ?
		WHERE id = ?`, nullStr(r.TaskID), nullStr(r.SessionID), string(r.Status), nullStr(string(r.WaitingReason)), nullStr(r.Summary),
		nullStr(r.Error), nullStr(r.LastOutput), boolInt(r.Reported), nullStr(string(r.ReportStatus)), boolInt(r.StopRequested),
		r.Usage.Nodes, r.Usage.Subagents, r.Usage.Depth, fmtTime(r.UpdatedAt), nullTime(r.EndedAt), r.ID)
	return r, err
}

// AddUsage adds to one run's usage counters (the pool is summed across the
// run tree by BudgetLeft).
func (s *Store) AddUsage(runID string, nodes, subagents, depth int) error {
	_, err := s.db.Exec(`UPDATE runs SET usage_nodes = usage_nodes + ?, usage_subagents = usage_subagents + ?,
		usage_depth = max(usage_depth, ?), updated_at = ? WHERE id = ?`, nodes, subagents, depth, fmtTime(s.now()), runID)
	return err
}

// BudgetLeft is what the run tree rooted at rootRunID may still spend.
func (s *Store) BudgetLeft(rootRunID string) (contract.BudgetLeft, error) {
	return s.budgetLeftLocked(rootRunID)
}

func (s *Store) budgetLeftLocked(rootRunID string) (contract.BudgetLeft, error) {
	root, err := s.GetRun(rootRunID)
	if err != nil {
		return contract.BudgetLeft{}, err
	}
	var nodes, subs int
	if err := s.db.QueryRow(`SELECT coalesce(sum(usage_nodes),0), coalesce(sum(usage_subagents),0) FROM runs WHERE root_run_id = ?`, rootRunID).
		Scan(&nodes, &subs); err != nil {
		return contract.BudgetLeft{}, err
	}
	left := func(max, used int) int {
		if max <= 0 {
			return -1
		}
		if used >= max {
			return 0
		}
		return max - used
	}
	return contract.BudgetLeft{
		Nodes:     left(root.Budget.MaxNodes, nodes),
		Subagents: left(root.Budget.MaxSubagents, subs),
		Depth:     root.Budget.MaxDepth,
	}, nil
}

// --- tokens ----------------------------------------------------------------------

// TokenInfo is a validated token's scope.
type TokenInfo struct {
	ID            string
	Kind          contract.TokenKind
	MapID         string // "" = all maps
	RunID         string
	ParticipantID string
	ShareLinkID   string
	ExpiresAt     time.Time
}

func newRawToken(kind contract.TokenKind, port int) (raw, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	raw = contract.FormatToken(kind, port, base64.RawURLEncoding.EncodeToString(b))
	return raw, hashToken(raw), nil
}

// IssueToken mints a token and stores only its hash. ttl 0 means until
// revoked. port is used by bridge kinds only.
func (s *Store) IssueToken(kind contract.TokenKind, port int, mapID, runID, participantID, shareLinkID, label string, ttl time.Duration) (string, TokenInfo, error) {
	raw, hash, err := newRawToken(kind, port)
	if err != nil {
		return "", TokenInfo{}, err
	}
	now := s.now()
	info := TokenInfo{ID: NewID("tk"), Kind: kind, MapID: mapID, RunID: runID, ParticipantID: participantID, ShareLinkID: shareLinkID}
	if ttl > 0 {
		info.ExpiresAt = now.Add(ttl)
	}
	_, err = s.db.Exec(`INSERT INTO tokens (id, kind, hash, map_id, run_id, participant_id, share_link_id, label, created_at, expires_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, info.ID, string(kind), hash, nullStr(mapID), nullStr(runID), nullStr(participantID),
		nullStr(shareLinkID), nullStr(label), fmtTime(now), nullTime(info.ExpiresAt))
	return raw, info, err
}

// ErrBadToken is returned for any missing, malformed, unknown, expired or
// revoked token. Callers map it to 401 or (guest routes) 404.
var ErrBadToken = contract.Errf(contract.ErrUnauthorized, "missing, expired or revoked token")

// LookupToken validates a raw token. The hash is looked up by index and
// then compared in constant time.
func (s *Store) LookupToken(raw string) (TokenInfo, error) {
	if _, ok := contract.ParseToken(raw); !ok {
		return TokenInfo{}, ErrBadToken
	}
	want := hashToken(raw)
	var info TokenInfo
	var got string
	var mapID, runID, pt, share, expires, revoked sql.NullString
	err := s.db.QueryRow(`SELECT id, kind, hash, map_id, run_id, participant_id, share_link_id, expires_at, revoked_at FROM tokens WHERE hash = ?`, want).
		Scan(&info.ID, &info.Kind, &got, &mapID, &runID, &pt, &share, &expires, &revoked)
	if err != nil || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 || revoked.Valid {
		return TokenInfo{}, ErrBadToken
	}
	info.MapID, info.RunID, info.ParticipantID, info.ShareLinkID = mapID.String, runID.String, pt.String, share.String
	info.ExpiresAt = parseTime(expires.String)
	if !info.ExpiresAt.IsZero() && s.now().After(info.ExpiresAt) {
		return TokenInfo{}, ErrBadToken
	}
	return info, nil
}

// RevokeToken revokes one token by id.
func (s *Store) RevokeToken(id string) error {
	_, err := s.db.Exec(`UPDATE tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, fmtTime(s.now()), id)
	return err
}

// RevokeRunTokens revokes every token of a run.
func (s *Store) RevokeRunTokens(runID string) error {
	_, err := s.db.Exec(`UPDATE tokens SET revoked_at = ? WHERE run_id = ? AND revoked_at IS NULL`, fmtTime(s.now()), runID)
	return err
}

// --- refs ------------------------------------------------------------------------

// SetRef binds an @ref to a node for a run tree.
func (s *Store) SetRef(rootRunID, ref, nodeID, runID string) error {
	_, err := s.db.Exec(`INSERT INTO run_refs (root_run_id, ref, node_id, created_by_run_id) VALUES (?,?,?,?)
		ON CONFLICT (root_run_id, ref) DO UPDATE SET node_id = excluded.node_id, created_by_run_id = excluded.created_by_run_id`,
		rootRunID, ref, nodeID, runID)
	return err
}

// ResolveRef returns the node bound to @ref in a run tree.
func (s *Store) ResolveRef(rootRunID, ref string) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT node_id FROM run_refs WHERE root_run_id = ? AND ref = ?`, rootRunID, ref).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", contract.Errf(contract.ErrNotFound, "unknown ref @%s; refs are set by add, restructure (regroup) and write_doc", ref)
	}
	return id, err
}

// --- patch blocks ----------------------------------------------------------------

// ClaimPatchBlock records that a mindmap-patch block is being processed. It
// returns false if the block was seen before (re-delivered message).
func (s *Store) ClaimPatchBlock(runID, messageID string, index int) (bool, error) {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO patch_blocks (run_id, message_id, block_index, ok, processed_at) VALUES (?,?,?,0,?)`,
		runID, messageID, index, fmtTime(s.now()))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// FinishPatchBlock records a block's outcome.
func (s *Store) FinishPatchBlock(runID, messageID string, index int, errs []string) error {
	var ej any
	if len(errs) > 0 {
		ej = mustJSON(errs)
	}
	_, err := s.db.Exec(`UPDATE patch_blocks SET ok = ?, error_json = ? WHERE run_id = ? AND message_id = ? AND block_index = ?`,
		boolInt(len(errs) == 0), ej, runID, messageID, index)
	return err
}

// --- activity --------------------------------------------------------------------

// AddActivity stores a feed event, filling id and ts when empty.
func (s *Store) AddActivity(ev contract.ActivityEvent) (contract.ActivityEvent, error) {
	if ev.ID == "" {
		ev.ID = NewID(contract.PrefixActivity)
	}
	if ev.TS.IsZero() {
		ev.TS = s.now()
	}
	ids := ev.NodeIDs
	if ids == nil {
		ids = []string{}
	}
	var count, rev any
	if ev.Count > 0 {
		count = ev.Count
	}
	if ev.Rev > 0 {
		rev = ev.Rev
	}
	_, err := s.db.Exec(`INSERT INTO activity (id, map_id, type, actor_participant_id, actor_run_id, summary, node_ids_json, count, rev, ts)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, ev.ID, ev.MapID, string(ev.Type), ev.Actor.ParticipantID, nullStr(ev.Actor.RunID), ev.Summary,
		mustJSON(ids), count, rev, fmtTime(ev.TS))
	return ev, err
}

// RecentActivity returns the latest n events, oldest first.
func (s *Store) RecentActivity(mapID string, n int) ([]contract.ActivityEvent, error) {
	rows, err := s.db.Query(`SELECT id, map_id, type, actor_participant_id, coalesce(actor_run_id,''), summary, node_ids_json,
		coalesce(count,0), coalesce(rev,0), ts FROM activity WHERE map_id = ? ORDER BY ts DESC, id DESC LIMIT ?`, mapID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// OpenQuestions reports whether a run has an unanswered ask_human question.
func (s *Store) OpenQuestions(mapID, runID string) (bool, error) {
	open := false
	err := s.View(mapID, func(_ contract.Map, nodes map[string]*contract.Node, _ map[string]*contract.Link) error {
		for _, n := range nodes {
			if !n.Deleted && n.Question != nil && n.Question.RunID == runID && n.Question.Open() {
				open = true
				return nil
			}
		}
		return nil
	})
	return open, err
}
