package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"

	"kandev-plugin-mindmap/server/contract"
)

// mapState is the materialised state of one map, kept in memory after first
// use. Nodes and links include tombstones (undo and restore need them); idx
// holds live nodes only.
type mapState struct {
	m     contract.Map
	nodes map[string]*contract.Node
	links map[string]*contract.Link
	idx   contract.ParentIndex
}

// state returns the cached state of mapID, loading it on first use. Caller
// holds s.mu.
func (s *Store) state(mapID string) (*mapState, error) {
	if st, ok := s.maps[mapID]; ok {
		return st, nil
	}
	st, err := s.loadState(mapID)
	if err != nil {
		return nil, err
	}
	s.maps[mapID] = st
	return st, nil
}

func (s *Store) loadState(mapID string) (*mapState, error) {
	m, err := s.readMap(mapID)
	if err != nil {
		return nil, err
	}
	st := &mapState{m: m, nodes: map[string]*contract.Node{}, links: map[string]*contract.Link{}, idx: contract.ParentIndex{}}
	rows, err := s.db.Query(`SELECT `+nodeCols+` FROM nodes WHERE map_id = ?`, mapID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		st.nodes[n.ID] = n
		if !n.Deleted {
			st.idx[n.ID] = n.ParentID
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	lrows, err := s.db.Query(`SELECT id, map_id, from_id, to_id, label, reason, author_participant_id, coalesce(author_run_id,''), deleted, created_at FROM links WHERE map_id = ?`, mapID)
	if err != nil {
		return nil, err
	}
	for lrows.Next() {
		var l contract.Link
		var del int
		var created string
		if err := lrows.Scan(&l.ID, &l.MapID, &l.From, &l.To, &l.Label, &l.Reason, &l.Author.ParticipantID, &l.Author.RunID, &del, &created); err != nil {
			lrows.Close()
			return nil, err
		}
		l.Deleted, l.CreatedAt = del == 1, parseTime(created)
		st.links[l.ID] = &l
	}
	lrows.Close()
	if err := lrows.Err(); err != nil {
		return nil, err
	}
	arows, err := s.db.Query(`SELECT `+assetCols+` FROM assets WHERE map_id = ? AND detached = 0 ORDER BY created_at, id`, mapID)
	if err != nil {
		return nil, err
	}
	defer arows.Close()
	for arows.Next() {
		a, err := scanAsset(arows)
		if err != nil {
			return nil, err
		}
		if n := st.nodes[a.NodeID]; n != nil {
			n.Assets = append(n.Assets, a)
		}
	}
	return st, arows.Err()
}

func (s *Store) readMap(mapID string) (contract.Map, error) {
	var m contract.Map
	var repo, ffMap sql.NullString
	var ffRev sql.NullInt64
	var settings, created, updated string
	err := s.db.QueryRow(`SELECT id, workspace_id, title, root_id, repo_id, settings_json, rev, forked_from_map_id, forked_from_rev, created_by, created_at, updated_at
		FROM maps WHERE id = ? AND deleted_at IS NULL`, mapID).
		Scan(&m.ID, &m.WorkspaceID, &m.Title, &m.RootID, &repo, &settings, &m.Rev, &ffMap, &ffRev, &m.CreatedBy, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return m, errNotFound("map %s not found", mapID)
	}
	if err != nil {
		return m, err
	}
	m.RepoID = repo.String
	_ = json.Unmarshal([]byte(settings), &m.Settings)
	if ffMap.Valid {
		m.ForkedFrom = &contract.ForkOrigin{MapID: ffMap.String, Rev: ffRev.Int64}
	}
	m.CreatedAt, m.UpdatedAt = parseTime(created), parseTime(updated)
	return m, nil
}

const nodeCols = `id, map_id, coalesce(parent_id,''), ord, title, body_md, kind, tags_json, confidence, sources_json, status,
	coalesce(run_id,''), author_participant_id, coalesce(author_run_id,''), collapsed, coalesce(color,''),
	coalesce(claim_participant_id,''), coalesce(claim_run_id,''), coalesce(claim_note,''), coalesce(claim_expires_at,''),
	coalesce(question_json,''), coalesce(doc_json,''), deleted, rev, created_at, updated_at`

type scanner interface{ Scan(...any) error }

func scanNode(r scanner) (*contract.Node, error) {
	var n contract.Node
	var tags, sources, cpt, crun, cnote, cexp, q, doc, created, updated string
	var conf sql.NullFloat64
	var collapsed, deleted int
	if err := r.Scan(&n.ID, &n.MapID, &n.ParentID, &n.Order, &n.Title, &n.BodyMD, &n.Kind, &tags, &conf, &sources, &n.Status,
		&n.RunID, &n.Author.ParticipantID, &n.Author.RunID, &collapsed, &n.Color, &cpt, &crun, &cnote, &cexp,
		&q, &doc, &deleted, &n.Rev, &created, &updated); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(tags), &n.Tags)
	_ = json.Unmarshal([]byte(sources), &n.Sources)
	if len(n.Tags) == 0 {
		n.Tags = nil
	}
	if len(n.Sources) == 0 {
		n.Sources = nil
	}
	if conf.Valid {
		c := conf.Float64
		n.Confidence = &c
	}
	if cpt != "" {
		n.Claim = &contract.Claim{ParticipantID: cpt, RunID: crun, Note: cnote, ExpiresAt: parseTime(cexp)}
	}
	if q != "" {
		n.Question = &contract.Question{}
		_ = json.Unmarshal([]byte(q), n.Question)
	}
	if doc != "" {
		n.Doc = &contract.DocMeta{}
		_ = json.Unmarshal([]byte(doc), n.Doc)
	}
	n.Collapsed, n.Deleted = collapsed == 1, deleted == 1
	n.CreatedAt, n.UpdatedAt = parseTime(created), parseTime(updated)
	return &n, nil
}

const assetCols = `id, node_id, kind, mime, bytes, sha256, coalesce(filename,''), coalesce(caption,''), coalesce(source_url,''),
	coalesce(width,0), coalesce(height,0), author_participant_id, coalesce(author_run_id,''), created_at`

func scanAsset(r scanner) (contract.Asset, error) {
	var a contract.Asset
	var created string
	err := r.Scan(&a.ID, &a.NodeID, &a.Kind, &a.MIME, &a.Bytes, &a.SHA256, &a.Filename, &a.Caption, &a.SourceURL,
		&a.Width, &a.Height, &a.Author.ParticipantID, &a.Author.RunID, &created)
	a.CreatedAt = parseTime(created)
	return a, err
}

// cloneNode deep-copies a node so journal entries are immune to later edits.
func cloneNode(n *contract.Node) *contract.Node {
	if n == nil {
		return nil
	}
	c := *n
	c.Tags = slices.Clone(n.Tags)
	c.Sources = slices.Clone(n.Sources)
	c.Assets = slices.Clone(n.Assets)
	if n.Confidence != nil {
		v := *n.Confidence
		c.Confidence = &v
	}
	if n.Claim != nil {
		v := *n.Claim
		c.Claim = &v
	}
	if n.Question != nil {
		v := *n.Question
		v.Options = slices.Clone(n.Question.Options)
		c.Question = &v
	}
	if n.Doc != nil {
		v := *n.Doc
		v.SourceIDs = slices.Clone(n.Doc.SourceIDs)
		c.Doc = &v
	}
	return &c
}

func cloneLink(l *contract.Link) *contract.Link {
	if l == nil {
		return nil
	}
	c := *l
	return &c
}

// children returns the live children of parent sorted by (order, id).
func (st *mapState) children(parent string) []*contract.Node {
	var out []*contract.Node
	for _, n := range st.nodes {
		if !n.Deleted && n.ParentID == parent && n.ID != st.m.RootID {
			out = append(out, n)
		}
	}
	sortSiblings(out)
	return out
}

func sortSiblings(ns []*contract.Node) {
	sort.Slice(ns, func(i, j int) bool {
		if ns[i].Order != ns[j].Order {
			return ns[i].Order < ns[j].Order
		}
		return ns[i].ID < ns[j].ID
	})
}

// childIndex builds parent -> sorted live children for the whole map.
func (st *mapState) childIndex() map[string][]*contract.Node {
	out := map[string][]*contract.Node{}
	for _, n := range st.nodes {
		if !n.Deleted && n.ParentID != "" {
			out[n.ParentID] = append(out[n.ParentID], n)
		}
	}
	for _, ns := range out {
		sortSiblings(ns)
	}
	return out
}

// lastChildOrder returns the order key after which a new last child goes.
func (st *mapState) lastChildOrder(parent string) string {
	last := ""
	for _, n := range st.nodes {
		if !n.Deleted && n.ParentID == parent && n.Order > last {
			last = n.Order
		}
	}
	return last
}

// liveNode returns a live node or a not_found/deleted error.
func (st *mapState) liveNode(field, id string) (*contract.Node, error) {
	n := st.nodes[id]
	if n == nil {
		return nil, contract.FieldErr(contract.ErrNotFound, field, "node %s not found", id)
	}
	if n.Deleted {
		return nil, contract.FieldErr(contract.ErrDeleted, field, "node %s was deleted", id)
	}
	return n, nil
}

// --- row writes -------------------------------------------------------------

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

func writeNode(tx execer, n *contract.Node) error {
	var conf any
	if n.Confidence != nil {
		conf = *n.Confidence
	}
	tags, sources := "[]", "[]"
	if len(n.Tags) > 0 {
		tags = mustJSON(n.Tags)
	}
	if len(n.Sources) > 0 {
		sources = mustJSON(n.Sources)
	}
	var cpt, crun, cnote, cexp any
	if n.Claim != nil {
		cpt, crun, cnote, cexp = n.Claim.ParticipantID, nullStr(n.Claim.RunID), nullStr(n.Claim.Note), fmtTime(n.Claim.ExpiresAt)
	}
	var q, doc any
	if n.Question != nil {
		q = mustJSON(n.Question)
	}
	if n.Doc != nil {
		doc = mustJSON(n.Doc)
	}
	var parent any
	if n.ParentID != "" {
		parent = n.ParentID
	}
	var rowid int64
	err := tx.QueryRow(`INSERT INTO nodes (id, map_id, parent_id, ord, title, body_md, kind, tags_json, confidence, sources_json, source_count,
		status, run_id, author_participant_id, author_run_id, collapsed, color, claim_participant_id, claim_run_id, claim_note, claim_expires_at,
		question_json, doc_json, deleted, rev, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (id) DO UPDATE SET parent_id=excluded.parent_id, ord=excluded.ord, title=excluded.title, body_md=excluded.body_md,
		kind=excluded.kind, tags_json=excluded.tags_json, confidence=excluded.confidence, sources_json=excluded.sources_json,
		source_count=excluded.source_count, status=excluded.status, run_id=excluded.run_id, collapsed=excluded.collapsed, color=excluded.color,
		claim_participant_id=excluded.claim_participant_id, claim_run_id=excluded.claim_run_id, claim_note=excluded.claim_note,
		claim_expires_at=excluded.claim_expires_at, question_json=excluded.question_json, doc_json=excluded.doc_json,
		deleted=excluded.deleted, rev=excluded.rev, updated_at=excluded.updated_at
		RETURNING rowid`,
		n.ID, n.MapID, parent, n.Order, n.Title, n.BodyMD, string(n.Kind), tags, conf, sources, len(n.Sources),
		string(n.Status), nullStr(n.RunID), n.Author.ParticipantID, nullStr(n.Author.RunID), boolInt(n.Collapsed), nullStr(n.Color),
		cpt, crun, cnote, cexp, q, doc, boolInt(n.Deleted), n.Rev, fmtTime(n.CreatedAt), fmtTime(n.UpdatedAt)).Scan(&rowid)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM nodes_fts WHERE rowid = ?`, rowid); err != nil {
		return err
	}
	if n.Deleted {
		return nil
	}
	titles := make([]string, 0, len(n.Sources))
	for _, src := range n.Sources {
		titles = append(titles, src.Title)
	}
	_, err = tx.Exec(`INSERT INTO nodes_fts (rowid, title, body_md, tags, source_titles) VALUES (?, ?, ?, ?, ?)`,
		rowid, n.Title, n.BodyMD, strings.Join(n.Tags, " "), strings.Join(titles, " "))
	return err
}

func writeLink(tx execer, l *contract.Link, rev int64) error {
	_, err := tx.Exec(`INSERT INTO links (id, map_id, from_id, to_id, label, reason, author_participant_id, author_run_id, deleted, rev, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (id) DO UPDATE SET from_id=excluded.from_id, to_id=excluded.to_id, label=excluded.label, reason=excluded.reason,
		deleted=excluded.deleted, rev=excluded.rev`,
		l.ID, l.MapID, l.From, l.To, l.Label, l.Reason, l.Author.ParticipantID, nullStr(l.Author.RunID), boolInt(l.Deleted), rev, fmtTime(l.CreatedAt))
	return err
}

// writeAsset inserts an asset row or, for the same map, updates its node,
// caption and detached flag. An id already used by another map's asset is
// a conflict: that row is left alone.
func writeAsset(tx execer, mapID string, a contract.Asset, detached bool) error {
	res, err := tx.Exec(`INSERT INTO assets (id, map_id, node_id, kind, mime, bytes, sha256, filename, caption, source_url, width, height,
		author_participant_id, author_run_id, detached, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (id) DO UPDATE SET node_id=excluded.node_id, caption=excluded.caption, detached=excluded.detached
		WHERE assets.map_id = excluded.map_id`,
		a.ID, mapID, a.NodeID, string(a.Kind), a.MIME, a.Bytes, a.SHA256, nullStr(a.Filename), nullStr(a.Caption), nullStr(a.SourceURL),
		a.Width, a.Height, a.Author.ParticipantID, nullStr(a.Author.RunID), boolInt(detached), fmtTime(a.CreatedAt))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return contract.FieldErr(contract.ErrConflict, "payload.asset.id", "asset id %s belongs to another map", a.ID)
	}
	return nil
}
