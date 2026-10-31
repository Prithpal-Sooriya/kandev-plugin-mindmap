package store

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"kandev-plugin-mindmap/server/contract"
)

// LockFunc reports whether field of nodeID is soft-locked by someone other
// than participantID. The hub answers it from live presence.
type LockFunc func(nodeID string, field contract.LockField, participantID string) bool

// SubmitOptions describes who is writing and how.
type SubmitOptions struct {
	Actor contract.Actor
	// BatchID groups the ops; minted when empty (and no op carries one).
	BatchID string
	// ProposalID is stamped on every op (accepting a proposal).
	ProposalID string
	// Locked is consulted for set/clear of title and body_md. Nil means no locks.
	Locked LockFunc
}

// SubmitResult is the outcome of a committed batch.
type SubmitResult struct {
	// Committed are the newly committed ops in rev order (duplicates excluded).
	Committed []contract.Op
	// Acks has one entry per submitted op, in submit order, including
	// re-submitted duplicates (acked with their original rev).
	Acks []contract.OpAck
	// Rebased are the committed ops the server changed (order key rewrites).
	Rebased []contract.Op
	Rev     int64
}

type journalEntry struct {
	kind     byte // 'n' node, 'l' link, 'm' map
	id       string
	prevNode *contract.Node
	prevLink *contract.Link
	prevMap  contract.Map
}

type assetWrite struct {
	asset    contract.Asset
	detached bool
}

type committedOp struct {
	op      contract.Op
	inverse []contract.Op
}

// txn applies ops to cached map state with an undo journal, then flushes
// the touched rows in one SQL transaction.
type txn struct {
	s           *Store
	st          *mapState
	now         time.Time
	rev         int64
	journal     []journalEntry
	dirtyNodes  map[string]bool
	dirtyLinks  map[string]bool
	assetWrites []assetWrite
	committed   []committedOp
	mapDirty    bool
}

type txnMark struct {
	j, a, c int
	rev     int64
}

func (s *Store) newTxn(st *mapState) *txn {
	return &txn{s: s, st: st, now: s.now(), rev: st.m.Rev, dirtyNodes: map[string]bool{}, dirtyLinks: map[string]bool{}}
}

func (t *txn) mark() txnMark {
	return txnMark{len(t.journal), len(t.assetWrites), len(t.committed), t.rev}
}

func (t *txn) touchNode(id string) {
	t.journal = append(t.journal, journalEntry{kind: 'n', id: id, prevNode: cloneNode(t.st.nodes[id])})
	t.dirtyNodes[id] = true
}

func (t *txn) touchLink(id string) {
	t.journal = append(t.journal, journalEntry{kind: 'l', id: id, prevLink: cloneLink(t.st.links[id])})
	t.dirtyLinks[id] = true
}

func (t *txn) touchMap() {
	m := t.st.m
	if t.st.m.ForkedFrom != nil {
		f := *t.st.m.ForkedFrom
		m.ForkedFrom = &f
	}
	t.journal = append(t.journal, journalEntry{kind: 'm', prevMap: m})
	t.mapDirty = true
}

// rollback restores state to mk.
func (t *txn) rollback(mk txnMark) {
	for i := len(t.journal) - 1; i >= mk.j; i-- {
		e := t.journal[i]
		switch e.kind {
		case 'n':
			if e.prevNode == nil {
				delete(t.st.nodes, e.id)
				delete(t.st.idx, e.id)
			} else {
				t.st.nodes[e.id] = e.prevNode
				if e.prevNode.Deleted {
					delete(t.st.idx, e.id)
				} else {
					t.st.idx[e.id] = e.prevNode.ParentID
				}
			}
		case 'l':
			if e.prevLink == nil {
				delete(t.st.links, e.id)
			} else {
				t.st.links[e.id] = e.prevLink
			}
		case 'm':
			t.st.m = e.prevMap
		}
	}
	t.journal = t.journal[:mk.j]
	t.assetWrites = t.assetWrites[:mk.a]
	t.committed = t.committed[:mk.c]
	t.rev = mk.rev
}

// flush writes every touched row and the committed ops in one transaction.
func (t *txn) flush() error {
	if len(t.committed) == 0 {
		return nil
	}
	tx, err := t.s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for id := range t.dirtyNodes {
		if n := t.st.nodes[id]; n != nil {
			if err := writeNode(tx, n); err != nil {
				return fmt.Errorf("write node %s: %w", id, err)
			}
		}
	}
	for id := range t.dirtyLinks {
		if l := t.st.links[id]; l != nil {
			if err := writeLink(tx, l, t.rev); err != nil {
				return fmt.Errorf("write link %s: %w", id, err)
			}
		}
	}
	for _, aw := range t.assetWrites {
		if err := writeAsset(tx, t.st.m.ID, aw.asset, aw.detached); err != nil {
			return fmt.Errorf("write asset %s: %w", aw.asset.ID, err)
		}
	}
	stmt, err := tx.Prepare(`INSERT INTO ops (map_id, rev, id, type, payload_json, inverse_json, actor_participant_id, actor_run_id, proposal_id, batch_id, base_rev, ts)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range t.committed {
		o := c.op
		payload, err := json.Marshal(o.Payload)
		if err != nil {
			return err
		}
		inv := "[]"
		if len(c.inverse) > 0 {
			b, err := json.Marshal(c.inverse)
			if err != nil {
				return err
			}
			inv = string(b)
		}
		var base any
		if o.BaseRev > 0 {
			base = o.BaseRev
		}
		if _, err := stmt.Exec(o.MapID, o.Rev, o.ID, string(o.Type), string(payload), inv, o.Actor.ParticipantID, nullStr(o.Actor.RunID),
			nullStr(o.ProposalID), nullStr(o.BatchID), base, fmtTime(o.TS)); err != nil {
			return fmt.Errorf("append op %s: %w", o.ID, err)
		}
	}
	m := t.st.m
	m.Rev, m.UpdatedAt = t.rev, t.now
	if _, err := tx.Exec(`UPDATE maps SET rev = ?, updated_at = ?, title = ?, repo_id = ?, settings_json = ? WHERE id = ?`,
		m.Rev, fmtTime(m.UpdatedAt), m.Title, nullStr(m.RepoID), mustJSON(m.Settings), m.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	t.st.m.Rev, t.st.m.UpdatedAt = m.Rev, m.UpdatedAt
	return nil
}

// Submit validates and applies ops as one atomic batch. Any failure rejects
// the whole batch and leaves state untouched; the error is a *contract.Error
// whose Field is prefixed with "ops[i].". Ops whose id is already in the log
// are acked with their original rev and not applied again.
func (s *Store) Submit(mapID string, ops []contract.Op, opt SubmitOptions) (SubmitResult, error) {
	if len(ops) == 0 {
		return SubmitResult{}, contract.FieldErr(contract.ErrBadRequest, "ops", "no ops")
	}
	if len(ops) > contract.MaxOpsPerBatch {
		return SubmitResult{}, contract.FieldErr(contract.ErrTooLarge, "ops", "at most %d ops per batch", contract.MaxOpsPerBatch)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.state(mapID)
	if err != nil {
		return SubmitResult{}, err
	}
	return s.submitLocked(st, ops, opt)
}

func (s *Store) submitLocked(st *mapState, ops []contract.Op, opt SubmitOptions) (SubmitResult, error) {
	ids := make([]string, 0, len(ops))
	for _, o := range ops {
		ids = append(ids, o.ID)
	}
	existing, err := s.existingOpRevs(st.m.ID, ids)
	if err != nil {
		return SubmitResult{}, err
	}
	batch := opt.BatchID
	if batch == "" {
		for _, o := range ops {
			if o.BatchID != "" && contract.ValidID(contract.PrefixBatch, o.BatchID) {
				batch = o.BatchID
				break
			}
		}
	}
	if batch == "" {
		batch = NewID(contract.PrefixBatch)
	}
	t := s.newTxn(st)
	res := SubmitResult{}
	seen := map[string]bool{}
	for i := range ops {
		o := ops[i]
		if rev, ok := existing[o.ID]; ok {
			res.Acks = append(res.Acks, contract.OpAck{ID: o.ID, Rev: rev})
			continue
		}
		if seen[o.ID] {
			t.rollback(txnMark{})
			return SubmitResult{}, contract.FieldErr(contract.ErrInvalidOp, fmt.Sprintf("ops[%d].id", i), "duplicate op id %s in batch", o.ID)
		}
		seen[o.ID] = true
		o.Actor, o.BatchID, o.ProposalID = opt.Actor, batch, opt.ProposalID
		out, rebased, err := t.apply(o, opt.Locked)
		if err != nil {
			t.rollback(txnMark{})
			return SubmitResult{}, prefixField(err, fmt.Sprintf("ops[%d]", i))
		}
		res.Acks = append(res.Acks, contract.OpAck{ID: out.ID, Rev: out.Rev})
		if rebased {
			res.Rebased = append(res.Rebased, out)
		}
	}
	if err := t.flush(); err != nil {
		t.rollback(txnMark{})
		return SubmitResult{}, contract.Errf(contract.ErrInternal, "commit: %v", err)
	}
	for _, c := range t.committed {
		res.Committed = append(res.Committed, c.op)
	}
	res.Rev = st.m.Rev
	return res, nil
}

func prefixField(err error, prefix string) error {
	e := *contract.AsError(err)
	if e.Field != "" {
		e.Field = prefix + "." + e.Field
	} else {
		e.Field = prefix
	}
	return &e
}

func (s *Store) existingOpRevs(mapID string, ids []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(ids) == 0 {
		return out, nil
	}
	q := `SELECT id, rev FROM ops WHERE map_id = ? AND id IN (?` + strings.Repeat(",?", len(ids)-1) + `)`
	args := []any{mapID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var rev int64
		if err := rows.Scan(&id, &rev); err != nil {
			return nil, err
		}
		out[id] = rev
	}
	return out, rows.Err()
}

// apply validates one op against current state and applies it. It returns
// the op as committed (rev, ts, possibly rewritten order) and whether the
// server changed it.
func (t *txn) apply(o contract.Op, locked LockFunc) (contract.Op, bool, error) {
	if o.Type == "" && o.Payload != nil {
		o.Type = o.Payload.OpType()
	}
	if o.MapID == "" {
		o.MapID = t.st.m.ID
	}
	if err := o.Validate(); err != nil {
		return o, false, err
	}
	if o.MapID != t.st.m.ID {
		return o, false, contract.FieldErr(contract.ErrForbidden, "map_id", "op is for map %s, not %s", o.MapID, t.st.m.ID)
	}
	if o.Actor.ParticipantID == "" {
		return o, false, contract.Errf(contract.ErrInternal, "op %s has no actor", o.ID)
	}
	o.Rev, o.TS = t.rev+1, t.now
	mk := t.mark()
	inv, rebased, err := t.mutate(&o, locked)
	if err != nil {
		t.rollback(mk)
		return o, false, err
	}
	for i := range inv {
		inv[i].MapID = o.MapID
	}
	t.rev = o.Rev
	t.committed = append(t.committed, committedOp{op: o, inverse: inv})
	return o, rebased, nil
}

func lockErr(field string) error {
	return contract.FieldErr(contract.ErrLocked, field, "someone else is editing this field; try again shortly")
}

// mutate applies o's payload. It may rewrite o (order keys) and returns the
// inverse ops for undo.
func (t *txn) mutate(o *contract.Op, locked LockFunc) ([]contract.Op, bool, error) {
	st := t.st
	actor := o.Actor
	switch p := o.Payload.(type) {
	case *contract.MapUpdatePayload:
		t.touchMap()
		inv := &contract.MapUpdatePayload{}
		if p.Title != nil {
			v := st.m.Title
			inv.Title = &v
			st.m.Title = *p.Title
		}
		if p.RepoID != nil {
			v := st.m.RepoID
			inv.RepoID = &v
			st.m.RepoID = *p.RepoID
		}
		if p.Settings != nil {
			v := st.m.Settings
			inv.Settings = &v
			st.m.Settings = *p.Settings
		}
		return []contract.Op{{Type: contract.OpMapUpdate, Payload: inv}}, false, nil

	case *contract.NodeCreatePayload:
		ni := p.Node
		if old := st.nodes[ni.ID]; old != nil {
			return nil, false, contract.FieldErr(contract.ErrConflict, "payload.node.id", "node %s already exists", ni.ID)
		}
		if par := st.nodes[ni.ParentID]; ni.ParentID != "" && par != nil && par.Deleted {
			return nil, false, contract.FieldErr(contract.ErrDeleted, "payload.node.parent_id", "parent %s was deleted", ni.ParentID)
		}
		if err := st.idx.CheckCreate(ni.ID, ni.ParentID); err != nil {
			return nil, false, err
		}
		if len(st.idx) >= contract.MaxMapNodes {
			return nil, false, contract.Errf(contract.ErrTooLarge, "map is full (%d nodes)", contract.MaxMapNodes)
		}
		order, rebased := t.resolveOrder(ni.ParentID, ni.ID, ni.Order)
		if rebased {
			p.Node.Order = order
		}
		status := ni.Status
		if status == "" {
			status = contract.StatusIdle
		}
		n := &contract.Node{
			ID: ni.ID, MapID: st.m.ID, ParentID: ni.ParentID, Order: order, Title: ni.Title, BodyMD: ni.BodyMD, Kind: ni.Kind,
			Tags: slices.Clone(ni.Tags), Confidence: ni.Confidence, Sources: slices.Clone(ni.Sources), Status: status, Author: actor,
			Collapsed: ni.Collapsed, Color: ni.Color, Question: ni.Question, Doc: ni.Doc, Rev: o.Rev, CreatedAt: t.now, UpdatedAt: t.now,
		}
		n = cloneNode(n)
		if n.Question != nil && n.Question.RunID == "" {
			n.Question.RunID = actor.RunID
		}
		t.touchNode(n.ID)
		st.nodes[n.ID] = n
		st.idx[n.ID] = n.ParentID
		return []contract.Op{{Type: contract.OpNodeDelete, Payload: &contract.NodeDeletePayload{ID: n.ID}}}, rebased, nil

	case *contract.NodeUpdatePayload:
		n, err := st.liveNode("payload.id", p.ID)
		if err != nil {
			return nil, false, err
		}
		if locked != nil {
			if p.Set != nil && p.Set.Title != nil && locked(n.ID, contract.LockTitle, actor.ParticipantID) {
				return nil, false, lockErr("payload.set.title")
			}
			bodyClear := slices.Contains(p.Clear, contract.ClearBody)
			if ((p.Set != nil && p.Set.BodyMD != nil) || bodyClear) && locked(n.ID, contract.LockBody, actor.ParticipantID) {
				return nil, false, lockErr("payload.set.body_md")
			}
		}
		t.touchNode(n.ID)
		prev := cloneNode(n)
		if err := applyNodeUpdate(n, p); err != nil {
			return nil, false, err
		}
		n.Rev, n.UpdatedAt = o.Rev, t.now
		inv := inverseUpdate(prev, p)
		if inv == nil {
			return nil, false, nil
		}
		return []contract.Op{{Type: contract.OpNodeUpdate, Payload: inv}}, false, nil

	case *contract.NodeMovePayload:
		n := st.nodes[p.ID]
		if n != nil && n.Deleted {
			return nil, false, contract.FieldErr(contract.ErrDeleted, "payload.id", "node %s was deleted", p.ID)
		}
		if par := st.nodes[p.ParentID]; par != nil && par.Deleted {
			return nil, false, contract.FieldErr(contract.ErrDeleted, "payload.parent_id", "parent %s was deleted", p.ParentID)
		}
		if err := st.idx.CheckMove(st.m.RootID, p.ID, p.ParentID); err != nil {
			return nil, false, prefixField(err, "payload")
		}
		order, rebased := t.resolveOrder(p.ParentID, p.ID, p.Order)
		if rebased {
			p.Order = order
		}
		inv := &contract.NodeMovePayload{ID: n.ID, ParentID: n.ParentID, Order: n.Order}
		t.touchNode(n.ID)
		n.ParentID, n.Order, n.Rev, n.UpdatedAt = p.ParentID, order, o.Rev, t.now
		st.idx[n.ID] = n.ParentID
		return []contract.Op{{Type: contract.OpNodeMove, Payload: inv}}, rebased, nil

	case *contract.NodeDeletePayload:
		if n := st.nodes[p.ID]; n != nil && n.Deleted {
			return nil, false, contract.FieldErr(contract.ErrDeleted, "payload.id", "node %s is already deleted", p.ID)
		}
		if err := st.idx.CheckDelete(st.m.RootID, p.ID); err != nil {
			return nil, false, prefixField(err, "payload")
		}
		var victims []string
		for id := range st.idx {
			if id == p.ID || st.idx.IsAncestor(p.ID, id) {
				victims = append(victims, id)
			}
		}
		for _, id := range victims {
			t.touchNode(id)
			n := st.nodes[id]
			n.Deleted, n.Rev, n.UpdatedAt = true, o.Rev, t.now
			delete(st.idx, id)
		}
		return []contract.Op{{Type: contract.OpNodeRestore, Payload: &contract.NodeRestorePayload{ID: p.ID}}}, false, nil

	case *contract.NodeRestorePayload:
		n := st.nodes[p.ID]
		if n == nil {
			return nil, false, contract.FieldErr(contract.ErrNotFound, "payload.id", "node %s not found", p.ID)
		}
		if !n.Deleted {
			return nil, false, contract.FieldErr(contract.ErrConflict, "payload.id", "node %s is not deleted", p.ID)
		}
		if _, ok := st.idx[n.ParentID]; !ok {
			return nil, false, contract.FieldErr(contract.ErrDeleted, "payload.id", "the parent of %s is deleted; restore it first", p.ID)
		}
		for _, id := range t.deletedSubtree(p.ID) {
			t.touchNode(id)
			c := st.nodes[id]
			c.Deleted, c.Rev, c.UpdatedAt = false, o.Rev, t.now
			st.idx[id] = c.ParentID
		}
		return []contract.Op{{Type: contract.OpNodeDelete, Payload: &contract.NodeDeletePayload{ID: p.ID}}}, false, nil

	case *contract.NodeMergePayload:
		return nil, false, t.merge(o, p)

	case *contract.NodeClaimPayload:
		n, err := st.liveNode("payload.id", p.ID)
		if err != nil {
			return nil, false, err
		}
		inv := claimInverse(n)
		t.touchNode(n.ID)
		c := p.Claim
		n.Claim, n.Rev, n.UpdatedAt = &c, o.Rev, t.now
		return inv, false, nil

	case *contract.NodeReleasePayload:
		n, err := st.liveNode("payload.id", p.ID)
		if err != nil {
			return nil, false, err
		}
		inv := claimInverse(n)
		t.touchNode(n.ID)
		n.Claim, n.Rev, n.UpdatedAt = nil, o.Rev, t.now
		return inv, false, nil

	case *contract.QuestionAnswerPayload:
		n, err := st.liveNode("payload.node_id", p.NodeID)
		if err != nil {
			return nil, false, err
		}
		if n.Question == nil {
			return nil, false, contract.FieldErr(contract.ErrInvalidOp, "payload.node_id", "node %s is not a question", p.NodeID)
		}
		if !n.Question.Open() {
			return nil, false, contract.FieldErr(contract.ErrConflict, "payload.node_id", "question already answered")
		}
		t.touchNode(n.ID)
		n.Question.Answer, n.Question.AnsweredBy, n.Question.AnsweredAt = p.Answer, actor.ParticipantID, t.now
		n.Rev, n.UpdatedAt = o.Rev, t.now
		return nil, false, nil

	case *contract.LinkCreatePayload:
		l := p.Link
		if st.links[l.ID] != nil {
			return nil, false, contract.FieldErr(contract.ErrConflict, "payload.link.id", "link %s already exists", l.ID)
		}
		if _, err := st.liveNode("payload.link.from", l.From); err != nil {
			return nil, false, err
		}
		if _, err := st.liveNode("payload.link.to", l.To); err != nil {
			return nil, false, err
		}
		t.touchLink(l.ID)
		st.links[l.ID] = &contract.Link{ID: l.ID, MapID: st.m.ID, From: l.From, To: l.To, Label: l.Label, Reason: l.Reason, Author: actor, CreatedAt: t.now}
		return []contract.Op{{Type: contract.OpLinkDelete, Payload: &contract.LinkDeletePayload{ID: l.ID}}}, false, nil

	case *contract.LinkUpdatePayload:
		l := st.links[p.ID]
		if l == nil || l.Deleted {
			return nil, false, contract.FieldErr(contract.ErrNotFound, "payload.id", "link %s not found", p.ID)
		}
		inv := &contract.LinkUpdatePayload{ID: l.ID}
		t.touchLink(l.ID)
		if p.Label != nil {
			v := l.Label
			inv.Label = &v
			l.Label = *p.Label
		}
		if p.Reason != nil {
			v := l.Reason
			inv.Reason = &v
			l.Reason = *p.Reason
		}
		return []contract.Op{{Type: contract.OpLinkUpdate, Payload: inv}}, false, nil

	case *contract.LinkDeletePayload:
		l := st.links[p.ID]
		if l == nil || l.Deleted {
			return nil, false, contract.FieldErr(contract.ErrNotFound, "payload.id", "link %s not found", p.ID)
		}
		t.touchLink(l.ID)
		l.Deleted = true
		return nil, false, nil

	case *contract.AssetAttachPayload:
		a := p.Asset
		n, err := st.liveNode("payload.asset.node_id", a.NodeID)
		if err != nil {
			return nil, false, err
		}
		for _, other := range st.nodes {
			for _, x := range other.Assets {
				if x.ID == a.ID {
					return nil, false, contract.FieldErr(contract.ErrConflict, "payload.asset.id", "asset %s is already attached", a.ID)
				}
			}
		}
		if _, err := os.Stat(t.s.blobPath(a.SHA256)); err != nil {
			return nil, false, contract.FieldErr(contract.ErrNotFound, "payload.asset.sha256", "no uploaded bytes with this hash; upload first")
		}
		if a.Author.ParticipantID == "" {
			a.Author = actor
		}
		if a.CreatedAt.IsZero() {
			a.CreatedAt = t.now
		}
		p.Asset = a
		t.touchNode(n.ID)
		n.Assets = append(n.Assets, a)
		n.Rev, n.UpdatedAt = o.Rev, t.now
		t.assetWrites = append(t.assetWrites, assetWrite{asset: a})
		return []contract.Op{{Type: contract.OpAssetDetach, Payload: &contract.AssetDetachPayload{ID: a.ID, NodeID: a.NodeID}}}, false, nil

	case *contract.AssetDetachPayload:
		n, err := st.liveNode("payload.node_id", p.NodeID)
		if err != nil {
			return nil, false, err
		}
		i := slices.IndexFunc(n.Assets, func(a contract.Asset) bool { return a.ID == p.ID })
		if i < 0 {
			return nil, false, contract.FieldErr(contract.ErrNotFound, "payload.id", "asset %s is not attached to %s", p.ID, p.NodeID)
		}
		a := n.Assets[i]
		t.touchNode(n.ID)
		n.Assets = slices.Delete(n.Assets, i, i+1)
		n.Rev, n.UpdatedAt = o.Rev, t.now
		t.assetWrites = append(t.assetWrites, assetWrite{asset: a, detached: true})
		return []contract.Op{{Type: contract.OpAssetAttach, Payload: &contract.AssetAttachPayload{Asset: a}}}, false, nil
	}
	return nil, false, contract.FieldErr(contract.ErrInvalidOp, "type", "unsupported op type %q", o.Type)
}

func claimInverse(n *contract.Node) []contract.Op {
	if n.Claim != nil {
		c := *n.Claim
		return []contract.Op{{Type: contract.OpNodeClaim, Payload: &contract.NodeClaimPayload{ID: n.ID, Claim: c}}}
	}
	return []contract.Op{{Type: contract.OpNodeRelease, Payload: &contract.NodeReleasePayload{ID: n.ID}}}
}

// resolveOrder returns order unchanged unless a live sibling under parent
// already uses it. Then the second writer's key is moved strictly between the
// winner and the next sibling (the protocol's order-collision rebase).
func (t *txn) resolveOrder(parent, self, order string) (string, bool) {
	collide := false
	next := ""
	for _, n := range t.st.nodes {
		if n.Deleted || n.ParentID != parent || n.ID == self || parent == "" {
			continue
		}
		if n.Order == order {
			collide = true
		} else if n.Order > order && (next == "" || n.Order < next) {
			next = n.Order
		}
	}
	if !collide {
		return order, false
	}
	return contract.OrderBetween(order, next), true
}

// deletedSubtree lists id and every tombstoned descendant reachable through
// tombstoned parents, so a restore brings back what a delete took.
func (t *txn) deletedSubtree(id string) []string {
	out := []string{id}
	for cid, n := range t.st.nodes {
		if cid == id || !n.Deleted {
			continue
		}
		cur := n.ParentID
		for range len(t.st.nodes) {
			if cur == id {
				out = append(out, cid)
				break
			}
			p := t.st.nodes[cur]
			if p == nil || !p.Deleted {
				break
			}
			cur = p.ParentID
		}
	}
	return out
}

func (t *txn) merge(o *contract.Op, p *contract.NodeMergePayload) error {
	st := t.st
	into, err := st.liveNode("payload.into_id", p.IntoID)
	if err != nil {
		return err
	}
	for i, id := range p.IDs {
		if _, err := st.liveNode(fmt.Sprintf("payload.ids[%d]", i), id); err != nil {
			return err
		}
	}
	if err := st.idx.CheckMerge(st.m.RootID, p.IntoID, p.IDs); err != nil {
		return prefixField(err, "payload")
	}
	t.touchNode(into.ID)
	last := st.lastChildOrder(into.ID)
	for _, id := range p.IDs {
		src := st.nodes[id]
		section := "## " + src.Title
		if src.BodyMD != "" {
			section += "\n\n" + src.BodyMD
		}
		if into.BodyMD == "" {
			into.BodyMD = section
		} else {
			into.BodyMD += "\n\n" + section
		}
		for _, tag := range src.Tags {
			if !slices.Contains(into.Tags, tag) {
				into.Tags = append(into.Tags, tag)
			}
		}
		into.Sources = mergeSources(into.Sources, src.Sources)
		for _, a := range src.Assets {
			a.NodeID = into.ID
			into.Assets = append(into.Assets, a)
			t.assetWrites = append(t.assetWrites, assetWrite{asset: a})
		}
		for _, c := range st.children(id) {
			t.touchNode(c.ID)
			last = contract.OrderBetween(last, "")
			c.ParentID, c.Order, c.Rev, c.UpdatedAt = into.ID, last, o.Rev, t.now
			st.idx[c.ID] = into.ID
		}
		for _, l := range st.links {
			if l.Deleted || (l.From != id && l.To != id) {
				continue
			}
			t.touchLink(l.ID)
			if l.From == id {
				l.From = into.ID
			}
			if l.To == id {
				l.To = into.ID
			}
			if l.From == l.To {
				l.Deleted = true
			}
		}
		t.touchNode(src.ID)
		src.Deleted, src.Assets, src.Rev, src.UpdatedAt = true, nil, o.Rev, t.now
		delete(st.idx, src.ID)
	}
	if len(into.BodyMD) > contract.MaxBodyLen {
		return contract.FieldErr(contract.ErrTooLarge, "payload.into_id", "merged body would exceed %d bytes", contract.MaxBodyLen)
	}
	if len(into.Tags) > contract.MaxTags {
		into.Tags = into.Tags[:contract.MaxTags]
	}
	if len(into.Sources) > contract.MaxSources {
		into.Sources = into.Sources[:contract.MaxSources]
	}
	into.Rev, into.UpdatedAt = o.Rev, t.now
	return nil
}

func mergeSources(dst, add []contract.Source) []contract.Source {
	for _, s := range add {
		if !slices.ContainsFunc(dst, func(x contract.Source) bool { return x.URL == s.URL }) {
			dst = append(dst, s)
		}
	}
	return dst
}

// applyNodeUpdate applies Set, then Clear, then the merge-style fields.
func applyNodeUpdate(n *contract.Node, p *contract.NodeUpdatePayload) error {
	if s := p.Set; s != nil {
		if s.Title != nil {
			n.Title = *s.Title
		}
		if s.BodyMD != nil {
			n.BodyMD = *s.BodyMD
		}
		if s.Kind != nil {
			n.Kind = *s.Kind
		}
		if s.Tags != nil {
			n.Tags = slices.Clone(*s.Tags)
		}
		if s.Confidence != nil {
			v := *s.Confidence
			n.Confidence = &v
		}
		if s.Status != nil {
			n.Status = *s.Status
		}
		if s.RunID != nil {
			n.RunID = *s.RunID
		}
		if s.Color != nil {
			n.Color = *s.Color
		}
		if s.Collapsed != nil {
			n.Collapsed = *s.Collapsed
		}
		if s.Doc != nil {
			d := *s.Doc
			n.Doc = &d
		}
	}
	for _, c := range p.Clear {
		switch c {
		case contract.ClearConfidence:
			n.Confidence = nil
		case contract.ClearColor:
			n.Color = ""
		case contract.ClearRunID:
			n.RunID = ""
		case contract.ClearBody:
			n.BodyMD = ""
		}
	}
	if p.AppendBody != "" {
		if n.BodyMD == "" {
			n.BodyMD = p.AppendBody
		} else {
			n.BodyMD += "\n\n" + p.AppendBody
		}
	}
	for _, tag := range p.AddTags {
		if !slices.Contains(n.Tags, tag) {
			n.Tags = append(n.Tags, tag)
		}
	}
	if len(p.RemoveTags) > 0 {
		n.Tags = slices.DeleteFunc(n.Tags, func(t string) bool { return slices.Contains(p.RemoveTags, t) })
	}
	n.Sources = mergeSources(n.Sources, p.AddSources)
	if len(n.Tags) == 0 {
		n.Tags = nil
	}
	switch {
	case len(n.BodyMD) > contract.MaxBodyLen:
		return contract.FieldErr(contract.ErrTooLarge, "payload.append_body", "body would exceed %d bytes", contract.MaxBodyLen)
	case len(n.Tags) > contract.MaxTags:
		return contract.FieldErr(contract.ErrTooLarge, "payload.add_tags", "more than %d tags", contract.MaxTags)
	case len(n.Sources) > contract.MaxSources:
		return contract.FieldErr(contract.ErrTooLarge, "payload.add_sources", "more than %d sources", contract.MaxSources)
	case n.Kind == contract.KindDoc && n.Doc == nil:
		return contract.FieldErr(contract.ErrInvalidOp, "payload.set.kind", "kind doc needs doc metadata")
	}
	return nil
}

// inverseUpdate builds the node.update that restores prev's values for every
// field p touched. add_sources is not reversible (NodeFields cannot set
// sources) and is left out. Returns nil when nothing can be inverted.
func inverseUpdate(prev *contract.Node, p *contract.NodeUpdatePayload) *contract.NodeUpdatePayload {
	set := &contract.NodeFields{}
	var clear []contract.ClearableField
	touched := false
	s := p.Set
	if s == nil {
		s = &contract.NodeFields{}
	}
	cleared := func(c contract.ClearableField) bool { return slices.Contains(p.Clear, c) }
	if s.Title != nil {
		v := prev.Title
		set.Title, touched = &v, true
	}
	if s.BodyMD != nil || cleared(contract.ClearBody) || p.AppendBody != "" {
		v := prev.BodyMD
		set.BodyMD, touched = &v, true
	}
	if s.Kind != nil {
		v := prev.Kind
		set.Kind, touched = &v, true
	}
	if s.Tags != nil || len(p.AddTags) > 0 || len(p.RemoveTags) > 0 {
		v := slices.Clone(prev.Tags)
		if v == nil {
			v = []string{}
		}
		set.Tags, touched = &v, true
	}
	if s.Confidence != nil || cleared(contract.ClearConfidence) {
		touched = true
		if prev.Confidence != nil {
			v := *prev.Confidence
			set.Confidence = &v
		} else {
			clear = append(clear, contract.ClearConfidence)
		}
	}
	if s.Status != nil {
		v := prev.Status
		set.Status, touched = &v, true
	}
	if s.RunID != nil || cleared(contract.ClearRunID) {
		touched = true
		if prev.RunID != "" {
			v := prev.RunID
			set.RunID = &v
		} else {
			clear = append(clear, contract.ClearRunID)
		}
	}
	if s.Color != nil || cleared(contract.ClearColor) {
		touched = true
		if prev.Color != "" {
			v := prev.Color
			set.Color = &v
		} else {
			clear = append(clear, contract.ClearColor)
		}
	}
	if s.Collapsed != nil {
		v := prev.Collapsed
		set.Collapsed, touched = &v, true
	}
	if s.Doc != nil && prev.Doc != nil {
		d := *prev.Doc
		set.Doc, touched = &d, true
	}
	if !touched {
		return nil
	}
	inv := &contract.NodeUpdatePayload{ID: prev.ID, Clear: clear}
	if *set != (contract.NodeFields{}) {
		inv.Set = set
	}
	return inv
}
