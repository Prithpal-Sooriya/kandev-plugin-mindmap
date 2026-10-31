package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"kandev-plugin-mindmap/server/contract"
)

// RootOrder is the order key of a map's root node.
const RootOrder = "V"

// CreateMap creates a map, its host participant and its root topic node
// (rev 1). It returns the map and the host participant.
func (s *Store) CreateMap(req contract.CreateMapRequest) (contract.Map, contract.Participant, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Untitled map"
	}
	if err := contract.ValidateTitle("title", title); err != nil {
		return contract.Map{}, contract.Participant{}, err
	}
	if req.WorkspaceID == "" {
		return contract.Map{}, contract.Participant{}, contract.FieldErr(contract.ErrBadRequest, "workspace_id", "required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, host, err := s.insertMap(req.WorkspaceID, title, req.RepoID, contract.MapSettings{}, nil)
	if err != nil {
		return m, host, err
	}
	st, err := s.state(m.ID)
	if err != nil {
		return m, host, err
	}
	root := contract.Op{ID: NewID(contract.PrefixOp), MapID: m.ID, Type: contract.OpNodeCreate, Payload: &contract.NodeCreatePayload{
		Node: contract.NodeInit{ID: m.RootID, Order: RootOrder, Title: title, Kind: contract.KindTopic},
	}}
	if _, err := s.submitLocked(st, []contract.Op{root}, SubmitOptions{Actor: contract.Actor{ParticipantID: host.ID}}); err != nil {
		return m, host, err
	}
	return st.m, host, nil
}

// insertMap writes the maps row and the host participant. Caller holds s.mu.
func (s *Store) insertMap(workspaceID, title, repoID string, settings contract.MapSettings, fork *contract.ForkOrigin) (contract.Map, contract.Participant, error) {
	now := s.now()
	m := contract.Map{ID: NewID(contract.PrefixMap), WorkspaceID: workspaceID, Title: title, RootID: NewID(contract.PrefixNode),
		RepoID: repoID, Settings: settings, ForkedFrom: fork, CreatedAt: now, UpdatedAt: now}
	host := contract.Participant{ID: NewID(contract.PrefixParticipant), MapID: m.ID, Kind: contract.ParticipantHuman, Name: "Host",
		Color: "#4f7cff", IsHost: true, JoinedAt: now, LastSeen: now}
	m.CreatedBy = host.ID
	tx, err := s.db.Begin()
	if err != nil {
		return m, host, err
	}
	defer func() { _ = tx.Rollback() }()
	var ffMap, ffRev any
	if fork != nil {
		ffMap, ffRev = fork.MapID, fork.Rev
	}
	if _, err := tx.Exec(`INSERT INTO maps (id, workspace_id, title, root_id, repo_id, settings_json, rev, forked_from_map_id, forked_from_rev, created_by, created_at, updated_at)
		VALUES (?,?,?,?,?,?,0,?,?,?,?,?)`, m.ID, m.WorkspaceID, m.Title, m.RootID, nullStr(m.RepoID), mustJSON(m.Settings), ffMap, ffRev,
		m.CreatedBy, fmtTime(now), fmtTime(now)); err != nil {
		return m, host, err
	}
	if err := insertParticipant(tx, host); err != nil {
		return m, host, err
	}
	return m, host, tx.Commit()
}

// GetMap returns a map's current header.
func (s *Store) GetMap(mapID string) (contract.Map, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.state(mapID)
	if err != nil {
		return contract.Map{}, err
	}
	return st.m, nil
}

// ListMaps lists maps, newest first. workspaceID "" lists every workspace;
// mapID restricts to one map (share-scoped tokens).
func (s *Store) ListMaps(workspaceID, mapID string) ([]contract.MapSummary, error) {
	q := `SELECT m.id, m.title, m.workspace_id, m.updated_at,
		(SELECT count(*) FROM nodes n WHERE n.map_id = m.id AND n.deleted = 0),
		(SELECT count(*) FROM runs r WHERE r.map_id = m.id AND r.status IN ('queued','running','waiting')),
		(SELECT count(*) FROM share_links sl WHERE sl.map_id = m.id AND sl.status IN ('starting','active'))
		FROM maps m WHERE m.deleted_at IS NULL`
	var args []any
	if workspaceID != "" {
		q += ` AND m.workspace_id = ?`
		args = append(args, workspaceID)
	}
	if mapID != "" {
		q += ` AND m.id = ?`
		args = append(args, mapID)
	}
	rows, err := s.db.Query(q+` ORDER BY m.updated_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contract.MapSummary{}
	for rows.Next() {
		var ms contract.MapSummary
		var updated string
		var shared int
		if err := rows.Scan(&ms.ID, &ms.Title, &ms.WorkspaceID, &updated, &ms.Nodes, &ms.ActiveRuns, &shared); err != nil {
			return nil, err
		}
		ms.UpdatedAt, ms.Shared = parseTime(updated), shared > 0
		out = append(out, ms)
	}
	return out, rows.Err()
}

// View runs fn with read access to a map's cached state under the store
// lock. fn must not retain the pointers it is given or call back into s.
func (s *Store) View(mapID string, fn func(m contract.Map, nodes map[string]*contract.Node, links map[string]*contract.Link) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.state(mapID)
	if err != nil {
		return err
	}
	return fn(st.m, st.nodes, st.links)
}

// Node returns a copy of one node (live or tombstoned).
func (s *Store) Node(mapID, nodeID string) (contract.Node, error) {
	var out contract.Node
	err := s.View(mapID, func(_ contract.Map, nodes map[string]*contract.Node, _ map[string]*contract.Link) error {
		n := nodes[nodeID]
		if n == nil {
			return errNotFound("node %s not found", nodeID)
		}
		out = *cloneNode(n)
		return nil
	})
	return out, err
}

// Snapshot returns the live state of a map. Presence is left empty for the
// caller (the hub) to fill.
func (s *Store) Snapshot(mapID string) (contract.Snapshot, error) {
	var snap contract.Snapshot
	pending, err := s.pendingProposalNodes(mapID)
	if err != nil {
		return snap, err
	}
	s.mu.Lock()
	st, err := s.state(mapID)
	if err != nil {
		s.mu.Unlock()
		return snap, err
	}
	snap.Rev, snap.Map = st.m.Rev, st.m
	snap.Nodes = make([]contract.Node, 0, len(st.idx))
	for _, n := range st.nodes {
		if n.Deleted {
			continue
		}
		c := *cloneNode(n)
		c.PendingProposalID = pending[c.ID]
		snap.Nodes = append(snap.Nodes, c)
	}
	snap.Links = []contract.Link{}
	for _, l := range st.links {
		if l.Deleted {
			continue
		}
		if _, ok := st.idx[l.From]; !ok {
			continue
		}
		if _, ok := st.idx[l.To]; !ok {
			continue
		}
		snap.Links = append(snap.Links, *l)
	}
	s.mu.Unlock()
	sort.Slice(snap.Nodes, func(i, j int) bool { return snap.Nodes[i].ID < snap.Nodes[j].ID })
	sort.Slice(snap.Links, func(i, j int) bool { return snap.Links[i].ID < snap.Links[j].ID })
	if snap.Runs, err = s.ListRuns(mapID); err != nil {
		return snap, err
	}
	if snap.Proposals, err = s.ListProposals(mapID, true); err != nil {
		return snap, err
	}
	if snap.Participants, err = s.ListParticipants(mapID); err != nil {
		return snap, err
	}
	if snap.Activity, err = s.RecentActivity(mapID, 50); err != nil {
		return snap, err
	}
	snap.Presence = []contract.Presence{}
	return snap, nil
}

// OpsSince returns committed ops with rev > since, oldest first, at most
// limit (0 = no limit).
func (s *Store) OpsSince(mapID string, since int64, limit int) ([]contract.Op, error) {
	q := `SELECT id, map_id, rev, type, payload_json, actor_participant_id, coalesce(actor_run_id,''), coalesce(proposal_id,''),
		coalesce(batch_id,''), coalesce(base_rev,0), ts FROM ops WHERE map_id = ? AND rev > ? ORDER BY rev`
	args := []any{mapID, since}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contract.Op{}
	for rows.Next() {
		var o contract.Op
		var payload, ts string
		if err := rows.Scan(&o.ID, &o.MapID, &o.Rev, &o.Type, &payload, &o.Actor.ParticipantID, &o.Actor.RunID, &o.ProposalID,
			&o.BatchID, &o.BaseRev, &ts); err != nil {
			return nil, err
		}
		p := contract.NewOpPayload(o.Type)
		if p == nil {
			return nil, contract.Errf(contract.ErrInternal, "op %s has unknown type %s", o.ID, o.Type)
		}
		if err := json.Unmarshal([]byte(payload), p); err != nil {
			return nil, err
		}
		o.Payload, o.TS = p, parseTime(ts)
		out = append(out, o)
	}
	return out, rows.Err()
}

// ForkMap copies the live state of mapID at rev (0 = head) into a new map in
// the same workspace. Node, link and asset ids are re-minted; statuses, runs
// and claims are not copied. The new map's log is a root create plus one
// batch recreating the tree.
func (s *Store) ForkMap(mapID string, rev int64) (contract.Map, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, err := s.state(mapID)
	if err != nil {
		return contract.Map{}, err
	}
	if rev < 0 || rev > src.m.Rev {
		return contract.Map{}, contract.FieldErr(contract.ErrBadRequest, "rev", "rev must be between 1 and %d", src.m.Rev)
	}
	view := src
	if rev > 0 && rev < src.m.Rev {
		if view, err = s.replay(src.m, rev); err != nil {
			return contract.Map{}, err
		}
	} else {
		rev = src.m.Rev
	}
	srcRoot := view.nodes[src.m.RootID]
	if srcRoot == nil {
		return contract.Map{}, contract.Errf(contract.ErrInternal, "map %s has no root at rev %d", mapID, rev)
	}
	m, host, err := s.insertMap(src.m.WorkspaceID, srcRoot.Title, src.m.RepoID, src.m.Settings, &contract.ForkOrigin{MapID: mapID, Rev: rev})
	if err != nil {
		return contract.Map{}, err
	}
	st, err := s.state(m.ID)
	if err != nil {
		return contract.Map{}, err
	}
	actor := contract.Actor{ParticipantID: host.ID}
	idMap := map[string]string{src.m.RootID: m.RootID}
	rootInit := forkInit(srcRoot, m.RootID, "")
	rootInit.Order = RootOrder
	ops := []contract.Op{{ID: NewID(contract.PrefixOp), MapID: m.ID, Type: contract.OpNodeCreate, Payload: &contract.NodeCreatePayload{Node: rootInit}}}
	for _, a := range srcRoot.Assets {
		ops = append(ops, forkAsset(m.ID, m.RootID, a))
	}
	kids := view.childIndex()
	queue := []string{src.m.RootID}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, c := range kids[pid] {
			nid := NewID(contract.PrefixNode)
			idMap[c.ID] = nid
			ops = append(ops, contract.Op{ID: NewID(contract.PrefixOp), MapID: m.ID, Type: contract.OpNodeCreate,
				Payload: &contract.NodeCreatePayload{Node: forkInit(c, nid, idMap[pid])}})
			for _, a := range c.Assets {
				ops = append(ops, forkAsset(m.ID, nid, a))
			}
			queue = append(queue, c.ID)
		}
	}
	for _, l := range view.links {
		from, to := idMap[l.From], idMap[l.To]
		if l.Deleted || from == "" || to == "" {
			continue
		}
		ops = append(ops, contract.Op{ID: NewID(contract.PrefixOp), MapID: m.ID, Type: contract.OpLinkCreate, Payload: &contract.LinkCreatePayload{
			Link: contract.LinkInit{ID: NewID(contract.PrefixLink), From: from, To: to, Label: l.Label, Reason: l.Reason}}})
	}
	if _, err := s.submitLocked(st, ops, SubmitOptions{Actor: actor}); err != nil {
		return contract.Map{}, err
	}
	return st.m, nil
}

func forkInit(n *contract.Node, id, parent string) contract.NodeInit {
	c := cloneNode(n)
	init := contract.NodeInit{ID: id, ParentID: parent, Order: c.Order, Title: c.Title, BodyMD: c.BodyMD, Kind: c.Kind, Tags: c.Tags,
		Confidence: c.Confidence, Sources: c.Sources, Color: c.Color, Collapsed: c.Collapsed, Question: c.Question, Doc: c.Doc}
	if init.Doc != nil {
		init.Doc.SourceIDs, init.Doc.RunID = nil, ""
	}
	if init.Question != nil {
		init.Question.RunID = ""
	}
	return init
}

func forkAsset(mapID, nodeID string, a contract.Asset) contract.Op {
	a.ID, a.NodeID = NewID(contract.PrefixAsset), nodeID
	return contract.Op{ID: NewID(contract.PrefixOp), MapID: mapID, Type: contract.OpAssetAttach, Payload: &contract.AssetAttachPayload{Asset: a}}
}

// replay rebuilds a map's state at rev from its op log, without touching the
// cache or the database.
func (s *Store) replay(m contract.Map, rev int64) (*mapState, error) {
	ops, err := s.OpsSince(m.ID, 0, int(rev))
	if err != nil {
		return nil, err
	}
	m.Rev = 0
	st := &mapState{m: m, nodes: map[string]*contract.Node{}, links: map[string]*contract.Link{}, idx: contract.ParentIndex{}}
	t := s.newTxn(st)
	for _, o := range ops {
		o.Rev = 0
		if _, _, err := t.apply(o, nil); err != nil {
			return nil, contract.Errf(contract.ErrInternal, "replay op %s: %v", o.ID, err)
		}
	}
	return st, nil
}

const undoMarker = "undo"

func undoBatchID(batch string) string { return hashID(contract.PrefixBatch, undoMarker, batch) }

// Undo reverts participantID's most recent batch on mapID that has not been
// undone yet, by committing its inverse ops as a new batch (never a rewind,
// so other people's later edits survive). Merges and question answers are
// not undoable.
func (s *Store) Undo(mapID, participantID string, opt SubmitOptions) (SubmitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.state(mapID)
	if err != nil {
		return SubmitResult{}, err
	}
	rows, err := s.db.Query(`SELECT id, coalesce(batch_id,''), type, inverse_json FROM ops WHERE map_id = ? AND actor_participant_id = ?
		ORDER BY rev DESC LIMIT 2000`, mapID, participantID)
	if err != nil {
		return SubmitResult{}, err
	}
	type entry struct {
		id, batch, typ, inv string
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.batch, &e.typ, &e.inv); err != nil {
			rows.Close()
			return SubmitResult{}, err
		}
		if e.batch == "" {
			e.batch = e.id
		}
		entries = append(entries, e)
	}
	rows.Close()
	undone := map[string]bool{}
	var target []entry
	for i := 0; i < len(entries); {
		j := i
		for j < len(entries) && entries[j].batch == entries[i].batch {
			j++
		}
		b := entries[i].batch
		if strings.HasPrefix(b, contract.PrefixBatch+"_"+undoMarker) {
			undone[b] = true
		} else if !undone[undoBatchID(b)] {
			target = entries[i:j]
			break
		}
		i = j
	}
	if target == nil {
		return SubmitResult{}, contract.Errf(contract.ErrConflict, "nothing to undo")
	}
	var inv []contract.Op
	for _, e := range target { // newest first: inverses apply in reverse order
		var ops []contract.Op
		if err := json.Unmarshal([]byte(e.inv), &ops); err != nil {
			return SubmitResult{}, err
		}
		if len(ops) == 0 && (e.typ == string(contract.OpNodeMerge) || e.typ == string(contract.OpQuestionAnswer) || e.typ == string(contract.OpLinkDelete)) {
			return SubmitResult{}, contract.Errf(contract.ErrConflict, "the last change (%s) cannot be undone", e.typ)
		}
		for _, o := range ops {
			o.ID, o.MapID = NewID(contract.PrefixOp), mapID
			inv = append(inv, o)
		}
	}
	if len(inv) == 0 {
		return SubmitResult{}, contract.Errf(contract.ErrConflict, "nothing to undo")
	}
	opt.BatchID = undoBatchID(target[0].batch)
	return s.submitLocked(st, inv, opt)
}

// MapWorkspace returns the workspace of a map without loading its state.
func (s *Store) MapWorkspace(mapID string) (string, error) {
	var ws string
	err := s.db.QueryRow(`SELECT workspace_id FROM maps WHERE id = ? AND deleted_at IS NULL`, mapID).Scan(&ws)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errNotFound("map %s not found", mapID)
	}
	return ws, err
}
