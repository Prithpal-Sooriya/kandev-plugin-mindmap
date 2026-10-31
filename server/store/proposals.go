package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"kandev-plugin-mindmap/server/contract"
)

const proposalCols = `id, map_id, run_id, base_rev, status, coalesce(summary,''), author_participant_id, coalesce(resolved_by,''),
	created_at, coalesce(resolved_at,'')`

func (s *Store) scanProposal(r scanner) (contract.Proposal, error) {
	var p contract.Proposal
	var created, resolved string
	if err := r.Scan(&p.ID, &p.MapID, &p.RunID, &p.BaseRev, &p.Status, &p.Summary, &p.Author.ParticipantID, &p.ResolvedBy, &created, &resolved); err != nil {
		return p, err
	}
	p.Author.RunID = p.RunID
	p.CreatedAt, p.ResolvedAt = parseTime(created), parseTime(resolved)
	items, err := s.proposalItems(p.ID)
	p.Items = items
	return p, err
}

type itemRow struct {
	seq  int
	item contract.ProposalItem
}

func (s *Store) proposalItemRows(id string) ([]itemRow, error) {
	rows, err := s.db.Query(`SELECT seq, op_json, status, node_ids_json, coalesce(error_json,'') FROM proposal_items WHERE proposal_id = ? ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []itemRow
	for rows.Next() {
		var r itemRow
		var op, ids, ej string
		if err := rows.Scan(&r.seq, &op, &r.item.Status, &ids, &ej); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(op), &r.item.Op); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(ids), &r.item.NodeIDs)
		if r.item.NodeIDs == nil {
			r.item.NodeIDs = []string{}
		}
		if ej != "" {
			r.item.Error = &contract.Error{}
			_ = json.Unmarshal([]byte(ej), r.item.Error)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) proposalItems(id string) ([]contract.ProposalItem, error) {
	rows, err := s.proposalItemRows(id)
	out := make([]contract.ProposalItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.item)
	}
	return out, err
}

// GetProposal returns a proposal with its items.
func (s *Store) GetProposal(id string) (contract.Proposal, error) {
	p, err := s.scanProposal(s.db.QueryRow(`SELECT `+proposalCols+` FROM proposals WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, errNotFound("proposal %s not found", id)
	}
	return p, err
}

// ListProposals lists a map's open and ready proposals, plus (when
// withRecent) the 20 most recently resolved ones.
func (s *Store) ListProposals(mapID string, withRecent bool) ([]contract.Proposal, error) {
	queries := []string{`SELECT id FROM proposals WHERE map_id = ? AND status IN ('open','ready') ORDER BY created_at`}
	if withRecent {
		queries = append(queries, `SELECT id FROM proposals WHERE map_id = ? AND status NOT IN ('open','ready') ORDER BY resolved_at DESC LIMIT 20`)
	}
	out := []contract.Proposal{}
	for _, q := range queries {
		rows, err := s.db.Query(q, mapID)
		if err != nil {
			return nil, err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			p, err := s.GetProposal(id)
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// pendingProposalNodes maps live node ids to the open/ready proposal that
// touches them (Node.PendingProposalID is derived, not stored).
func (s *Store) pendingProposalNodes(mapID string) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT i.proposal_id, i.node_ids_json FROM proposal_items i JOIN proposals p ON p.id = i.proposal_id
		WHERE p.map_id = ? AND p.status IN ('open','ready') AND i.status = 'pending'`, mapID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var pid, ids string
		if err := rows.Scan(&pid, &ids); err != nil {
			return nil, err
		}
		var ns []string
		_ = json.Unmarshal([]byte(ids), &ns)
		for _, n := range ns {
			if _, ok := out[n]; !ok {
				out[n] = pid
			}
		}
	}
	return out, rows.Err()
}

// Propose validates ops against the live map plus the proposal's pending
// items (so later items can reference ghosts created by earlier ones) and
// appends them as pending items. Nothing is applied to the map. It returns
// the ops as stored (order keys may have been rewritten).
func (s *Store) Propose(proposalID string, ops []contract.Op, actor contract.Actor) ([]contract.Op, error) {
	p, err := s.GetProposal(proposalID)
	if err != nil {
		return nil, err
	}
	if p.Status != contract.ProposalOpen && p.Status != contract.ProposalReady {
		return nil, contract.Errf(contract.ErrProposalClosed, "proposal %s is %s", p.ID, p.Status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.state(p.MapID)
	if err != nil {
		return nil, err
	}
	rows, err := s.proposalItemRows(p.ID)
	if err != nil {
		return nil, err
	}
	t := s.newTxn(st)
	defer t.rollback(txnMark{})
	for _, r := range rows {
		if r.item.Status != contract.ItemPending {
			continue
		}
		mk := t.mark()
		if _, _, err := t.apply(r.item.Op, nil); err != nil {
			t.rollback(mk)
		}
	}
	out := make([]contract.Op, 0, len(ops))
	for i, o := range ops {
		o.Actor, o.ProposalID, o.MapID = actor, p.ID, p.MapID
		applied, _, err := t.apply(o, nil)
		if err != nil {
			return nil, prefixField(err, "ops["+itoa(i)+"]")
		}
		o.Payload, o.Rev, o.TS = applied.Payload, 0, s.now()
		out = append(out, o)
	}
	seq := 0
	if len(rows) > 0 {
		seq = rows[len(rows)-1].seq
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, o := range out {
		seq++
		ids := uniq(o.TouchedNodeIDs())
		if _, err := tx.Exec(`INSERT INTO proposal_items (proposal_id, seq, op_id, op_json, status, node_ids_json) VALUES (?,?,?,?,?,?)`,
			p.ID, seq, o.ID, mustJSON(o), string(contract.ItemPending), mustJSON(ids)); err != nil {
			return nil, err
		}
	}
	return out, tx.Commit()
}

func uniq(ids []string) []string {
	out := []string{}
	for _, id := range ids {
		if id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// SetProposalReady moves an open proposal to ready (the run reported) and
// stores its summary.
func (s *Store) SetProposalReady(id, summary string) error {
	_, err := s.db.Exec(`UPDATE proposals SET status = 'ready', summary = ? WHERE id = ? AND status = 'open'`, nullStr(summary), id)
	return err
}

// ReviewResult is the outcome of a proposal review.
type ReviewResult struct {
	Proposal  contract.Proposal
	Committed []contract.Op
	Rev       int64
}

// ReviewProposal resolves items. Accepted items are applied in their
// original order as one batch, each with proposal_id set and the agent as
// actor. An accepted item that no longer applies, or that depends on a node
// created by a rejected or conflicting item, becomes conflict; the rest still
// apply.
func (s *Store) ReviewProposal(id string, req contract.ReviewProposalRequest, reviewer string) (ReviewResult, error) {
	p, err := s.GetProposal(id)
	if err != nil {
		return ReviewResult{}, err
	}
	if p.Status.Resolved() {
		return ReviewResult{}, contract.Errf(contract.ErrProposalClosed, "proposal %s is already %s", p.ID, p.Status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.state(p.MapID)
	if err != nil {
		return ReviewResult{}, err
	}
	rows, err := s.proposalItemRows(p.ID)
	if err != nil {
		return ReviewResult{}, err
	}
	decide := func(opID string) contract.ProposalItemStatus {
		switch {
		case req.AcceptAll || slices.Contains(req.Accept, opID):
			return contract.ItemAccepted
		case req.RejectAll || slices.Contains(req.Reject, opID):
			return contract.ItemRejected
		}
		return contract.ItemPending
	}
	t := s.newTxn(st)
	dead := map[string]bool{}
	batch := NewID(contract.PrefixBatch)
	applied := map[int]int64{}
	for i := range rows {
		it := &rows[i].item
		if it.Status != contract.ItemPending {
			continue
		}
		d := decide(it.Op.ID)
		created := ""
		if c, ok := it.Op.Payload.(*contract.NodeCreatePayload); ok {
			created = c.Node.ID
		}
		switch d {
		case contract.ItemPending:
			continue
		case contract.ItemRejected:
			it.Status = contract.ItemRejected
			if created != "" {
				dead[created] = true
			}
			continue
		}
		if dep := slices.IndexFunc(it.NodeIDs, func(n string) bool { return dead[n] }); dep >= 0 {
			it.Status, it.Error = contract.ItemConflict, contract.Errf(contract.ErrConflict, "depends on node %s, which was not accepted", it.NodeIDs[dep])
			if created != "" {
				dead[created] = true
			}
			continue
		}
		o := it.Op
		o.ProposalID, o.BatchID, o.Rev = p.ID, batch, 0
		mk := t.mark()
		out, _, err := t.apply(o, nil)
		if err != nil {
			t.rollback(mk)
			it.Status, it.Error = contract.ItemConflict, contract.AsError(err)
			if created != "" {
				dead[created] = true
			}
			continue
		}
		it.Status, it.Op = contract.ItemAccepted, out
		applied[i] = out.Rev
	}
	if err := t.flush(); err != nil {
		t.rollback(txnMark{})
		return ReviewResult{}, contract.Errf(contract.ErrInternal, "commit: %v", err)
	}
	status := contract.ProposalStatus("")
	nAcc, nRej, nPend := 0, 0, 0
	for _, r := range rows {
		switch r.item.Status {
		case contract.ItemPending:
			nPend++
		case contract.ItemAccepted:
			nAcc++
		default:
			nRej++
		}
	}
	switch {
	case nPend > 0:
		status = p.Status
	case nAcc > 0 && nRej == 0:
		status = contract.ProposalAccepted
	case nAcc == 0:
		status = contract.ProposalRejected
	default:
		status = contract.ProposalPartial
	}
	tx, err := s.db.Begin()
	if err != nil {
		return ReviewResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	for i, r := range rows {
		var ej, rev any
		if r.item.Error != nil {
			ej = mustJSON(r.item.Error)
		}
		if v, ok := applied[i]; ok {
			rev = v
		}
		if _, err := tx.Exec(`UPDATE proposal_items SET status = ?, error_json = ?, applied_rev = coalesce(?, applied_rev), op_json = ?
			WHERE proposal_id = ? AND seq = ?`, string(r.item.Status), ej, rev, mustJSON(r.item.Op), p.ID, r.seq); err != nil {
			return ReviewResult{}, err
		}
	}
	if status.Resolved() {
		if _, err := tx.Exec(`UPDATE proposals SET status = ?, resolved_by = ?, resolved_at = ? WHERE id = ?`,
			string(status), reviewer, fmtTime(s.now()), p.ID); err != nil {
			return ReviewResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ReviewResult{}, err
	}
	res := ReviewResult{Rev: st.m.Rev}
	for _, c := range t.committed {
		res.Committed = append(res.Committed, c.op)
	}
	res.Proposal, err = s.GetProposal(p.ID)
	return res, err
}
