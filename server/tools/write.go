package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"kandev-plugin-mindmap/server/app"
	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/store"
)

// treeInfo is what write tools need to know about the map before building
// ops: parent index, order keys and node titles.
type treeInfo struct {
	rootID string
	idx    contract.ParentIndex
	nodes  map[string]contract.Node // shallow copies
}

func (t *Tools) tree(mapID string) (treeInfo, error) {
	ti := treeInfo{idx: contract.ParentIndex{}, nodes: map[string]contract.Node{}}
	err := t.svc.Store.View(mapID, func(m contract.Map, nodes map[string]*contract.Node, _ map[string]*contract.Link) error {
		ti.rootID = m.RootID
		for id, n := range nodes {
			if n.Deleted {
				continue
			}
			ti.idx[id] = n.ParentID
			c := *n
			c.BodyMD, c.Sources, c.Assets = "", nil, nil
			ti.nodes[id] = c
		}
		return nil
	})
	return ti, err
}

// lastChild returns the largest order key under parent.
func (ti treeInfo) lastChild(parent string) string {
	last := ""
	for id, p := range ti.idx {
		if p == parent && ti.nodes[id].Order > last {
			last = ti.nodes[id].Order
		}
	}
	return last
}

// after returns the key of after and of its next sibling under parent.
func (ti treeInfo) after(parent, after string) (string, string, error) {
	a, ok := ti.nodes[after]
	if !ok || ti.idx[after] != parent {
		return "", "", fmt.Errorf("after_id %s is not a child of %s", after, parent)
	}
	next := ""
	for id, p := range ti.idx {
		o := ti.nodes[id].Order
		if p == parent && o > a.Order && (next == "" || o < next) {
			next = o
		}
	}
	return a.Order, next, nil
}

// relDepth is the depth of id below the run node, or -1 if id is outside
// the run node's subtree.
func (ti treeInfo) relDepth(runNode, id string) int {
	if id == runNode {
		return 0
	}
	if !ti.idx.IsAncestor(runNode, id) {
		return -1
	}
	return ti.idx.Depth(id) - ti.idx.Depth(runNode)
}

// orderSeq hands out increasing keys after a starting key (and before an
// optional upper bound).
type orderSeq struct{ last, upper string }

func (o *orderSeq) next() string {
	o.last = contract.OrderBetween(o.last, o.upper)
	return o.last
}

func maxNest(ns []contract.AddNode) int {
	d := 0
	for _, n := range ns {
		d = max(d, 1+maxNest(n.Children))
	}
	return d
}

func (t *Tools) add(_ context.Context, s *session, a any) (contract.ToolResult, error) {
	args := a.(*contract.AddArgs)
	def := ""
	if s.run != nil {
		def = s.run.NodeID
	}
	parent, err := t.writeNode(s, "parent_id", args.ParentID, def)
	if err != nil {
		return contract.ToolResult{}, err
	}
	ti, err := t.tree(s.mapID)
	if err != nil {
		return contract.ToolResult{}, err
	}
	count, nest := args.CountNodes(), maxNest(args.Nodes)
	depthUsed := 0
	if s.run != nil {
		left := t.budget(s)
		if left != nil && left.Nodes >= 0 && count > left.Nodes {
			return contract.ToolResult{}, contract.FieldErr(contract.ErrBudgetExceeded, "nodes",
				"this call adds %d nodes but the run has %d left; add fewer or ask a human", count, left.Nodes)
		}
		if rd := ti.relDepth(s.run.NodeID, parent); rd >= 0 {
			depthUsed = rd + nest
			if s.run.Budget.MaxDepth > 0 && depthUsed > s.run.Budget.MaxDepth {
				return contract.ToolResult{}, contract.FieldErr(contract.ErrBudgetExceeded, "nodes",
					"this would reach %d levels below your node; the budget allows %d", depthUsed, s.run.Budget.MaxDepth)
			}
		}
	}
	seq := &orderSeq{last: ti.lastChild(parent)}
	if args.AfterID != "" {
		after, err := t.writeNode(s, "after_id", args.AfterID, "")
		if err != nil {
			return contract.ToolResult{}, err
		}
		lo, hi, err := ti.after(parent, after)
		if err != nil {
			return contract.ToolResult{}, contract.FieldErr(contract.ErrBadRequest, "after_id", "%v", err)
		}
		seq = &orderSeq{last: lo, upper: hi}
	}
	var ops []contract.Op
	refs := map[string]string{}
	var ids []string
	var lines []string
	var build func(parent string, ns []contract.AddNode, seq *orderSeq, depth int)
	build = func(parent string, ns []contract.AddNode, seq *orderSeq, depth int) {
		for _, n := range ns {
			id := store.NewID(contract.PrefixNode)
			kind := n.Kind
			if kind == "" {
				kind = contract.KindIdea
			}
			ops = append(ops, newOp(s.mapID, &contract.NodeCreatePayload{Node: contract.NodeInit{
				ID: id, ParentID: parent, Order: seq.next(), Title: n.Title, BodyMD: n.Body, Kind: kind, Tags: n.Tags,
				Confidence: n.Confidence, Sources: n.Sources,
			}}))
			ids = append(ids, id)
			l := fmt.Sprintf("%s- [%s] %s", strings.Repeat("  ", depth), id, n.Title)
			if n.Ref != "" {
				refs[n.Ref] = id
				l += " (@" + n.Ref + ")"
			}
			lines = append(lines, l)
			build(id, n.Children, &orderSeq{}, depth+1)
		}
	}
	build(parent, args.Nodes, seq, 0)
	out, err := t.commit(s, ops, false)
	if err != nil {
		return contract.ToolResult{}, err
	}
	t.setRefs(s, refs)
	if s.run != nil {
		_ = t.svc.Store.AddUsage(s.run.ID, count, 0, depthUsed)
		out.Budget = t.budget(s)
	}
	out.NodeIDs, out.Refs = ids, refs
	t.focus(s, ids[0])
	if out.Applied {
		t.activity(s, contract.ActivityNodesAdded, fmt.Sprintf("added %d nodes under \"%s\"", count, ti.nodes[parent].Title), ids, count, out.Rev)
	}
	return outcomeResult(fmt.Sprintf("added %d nodes under [%s]:\n%s", count, parent, strings.Join(lines, "\n")), out), nil
}

func (t *Tools) update(_ context.Context, s *session, a any) (contract.ToolResult, error) {
	args := a.(*contract.UpdateArgs)
	id, err := t.writeNode(s, "node_id", args.NodeID, "")
	if err != nil {
		return contract.ToolResult{}, err
	}
	p := &contract.NodeUpdatePayload{ID: id, AppendBody: args.AppendBody, AddTags: args.AddTags, RemoveTags: args.RemoveTags, AddSources: args.AddSources}
	set := contract.NodeFields{}
	if args.Title != "" {
		v := args.Title
		set.Title = &v
	}
	if args.Body != nil {
		v := *args.Body
		set.BodyMD = &v
	}
	if args.Kind != "" {
		v := args.Kind
		set.Kind = &v
	}
	if args.Confidence != nil {
		v := *args.Confidence
		set.Confidence = &v
	}
	if set != (contract.NodeFields{}) {
		p.Set = &set
	}
	out, err := t.commit(s, []contract.Op{newOp(s.mapID, p)}, false)
	if err != nil {
		return contract.ToolResult{}, err
	}
	out.NodeIDs = []string{id}
	t.focus(s, id)
	if out.Applied {
		t.activity(s, contract.ActivityNodesUpdated, "updated a node", []string{id}, 1, out.Rev)
	}
	return outcomeResult(fmt.Sprintf("updated [%s]", id), out), nil
}

func (t *Tools) restructure(_ context.Context, s *session, a any) (contract.ToolResult, error) {
	args := a.(*contract.RestructureArgs)
	var ops []contract.Op
	var lines []string
	touched := []string{}
	refs := map[string]string{}
	var ti treeInfo
	lastKey := map[string]string{} // parent -> last key handed out in this call
	appendKey := func(parent string) string {
		k, ok := lastKey[parent]
		if !ok {
			k = ti.lastChild(parent)
		}
		k = contract.OrderBetween(k, "")
		lastKey[parent] = k
		return k
	}
	for i, op := range args.Ops {
		f := fmt.Sprintf("ops[%d]", i)
		need := func(field, arg string) (string, error) { return t.writeNode(s, f+"."+field, arg, "") }
		if i == 0 {
			// Bind the map (personal tokens) before reading the tree.
			first := op.NodeID
			if first == "" && len(op.NodeIDs) > 0 {
				first = op.NodeIDs[0]
			}
			if first == "" {
				first = op.IntoID
			}
			if _, err := need("node_id", first); err != nil {
				return contract.ToolResult{}, err
			}
			var err error
			if ti, err = t.tree(s.mapID); err != nil {
				return contract.ToolResult{}, err
			}
		}
		switch op.Op {
		case contract.RestructureMove:
			id, err := need("node_id", op.NodeID)
			if err != nil {
				return contract.ToolResult{}, err
			}
			parent, err := need("parent_id", op.ParentID)
			if err != nil {
				return contract.ToolResult{}, err
			}
			key := ""
			if op.AfterID != "" {
				after, err := need("after_id", op.AfterID)
				if err != nil {
					return contract.ToolResult{}, err
				}
				lo, hi, err := ti.after(parent, after)
				if err != nil {
					return contract.ToolResult{}, contract.FieldErr(contract.ErrBadRequest, f+".after_id", "%v", err)
				}
				key = contract.OrderBetween(lo, hi)
			} else {
				key = appendKey(parent)
			}
			ops = append(ops, newOp(s.mapID, &contract.NodeMovePayload{ID: id, ParentID: parent, Order: key}))
			touched = append(touched, id)
			lines = append(lines, fmt.Sprintf("move [%s] under [%s]", id, parent))
		case contract.RestructureMerge:
			into, err := need("into_id", op.IntoID)
			if err != nil {
				return contract.ToolResult{}, err
			}
			var ids []string
			for j, x := range op.NodeIDs {
				id, err := need(fmt.Sprintf("node_ids[%d]", j), x)
				if err != nil {
					return contract.ToolResult{}, err
				}
				ids = append(ids, id)
			}
			ops = append(ops, newOp(s.mapID, &contract.NodeMergePayload{IntoID: into, IDs: ids}))
			touched = append(touched, into)
			lines = append(lines, fmt.Sprintf("merge %v into [%s]", ids, into))
		case contract.RestructureRegroup:
			var ids []string
			for j, x := range op.NodeIDs {
				id, err := need(fmt.Sprintf("node_ids[%d]", j), x)
				if err != nil {
					return contract.ToolResult{}, err
				}
				ids = append(ids, id)
			}
			parent := ti.idx[ids[0]]
			if op.ParentID != "" {
				p, err := need("parent_id", op.ParentID)
				if err != nil {
					return contract.ToolResult{}, err
				}
				parent = p
			}
			if parent == "" {
				parent = ti.rootID
			}
			kind := op.Kind
			if kind == "" || kind == contract.KindDoc {
				kind = contract.KindTopic
			}
			gid := store.NewID(contract.PrefixNode)
			body := ""
			if op.Reason != "" {
				body = op.Reason
			}
			ops = append(ops, newOp(s.mapID, &contract.NodeCreatePayload{Node: contract.NodeInit{ID: gid, ParentID: parent, Order: appendKey(parent),
				Title: op.Title, Kind: kind, BodyMD: body}}))
			for _, id := range ids {
				ops = append(ops, newOp(s.mapID, &contract.NodeMovePayload{ID: id, ParentID: gid, Order: appendKey(gid)}))
			}
			s.pending[gid] = true
			ti.idx[gid] = parent
			if op.Ref != "" {
				refs[op.Ref] = gid
				s.refs[op.Ref] = gid
			}
			touched = append(touched, gid)
			lines = append(lines, fmt.Sprintf("regroup %d nodes under new [%s] %s", len(ids), gid, op.Title))
		case contract.RestructureRename:
			id, err := need("node_id", op.NodeID)
			if err != nil {
				return contract.ToolResult{}, err
			}
			title := op.Title
			ops = append(ops, newOp(s.mapID, &contract.NodeUpdatePayload{ID: id, Set: &contract.NodeFields{Title: &title}}))
			touched = append(touched, id)
			lines = append(lines, fmt.Sprintf("rename [%s] to %s", id, title))
		case contract.RestructureRetire:
			id, err := need("node_id", op.NodeID)
			if err != nil {
				return contract.ToolResult{}, err
			}
			ops = append(ops, newOp(s.mapID, &contract.NodeDeletePayload{ID: id}))
			touched = append(touched, id)
			lines = append(lines, fmt.Sprintf("retire [%s] (soft-deleted; undo restores it)", id))
		}
	}
	batch := store.NewID(contract.PrefixBatch)
	for i := range ops {
		ops[i].BatchID = batch
	}
	out, err := t.commit(s, ops, false)
	if err != nil {
		return contract.ToolResult{}, err
	}
	t.setRefs(s, refs)
	out.NodeIDs, out.Refs = uniq(touched), refs
	if len(out.Refs) == 0 {
		out.Refs = nil
	}
	if len(touched) > 0 {
		t.focus(s, touched[0])
	}
	if out.Applied {
		t.activity(s, contract.ActivityNodesRestructured, "reorganised the map: "+args.Reason, uniq(touched), len(args.Ops), out.Rev)
	}
	return outcomeResult("restructured ("+args.Reason+"):\n"+strings.Join(lines, "\n"), out), nil
}

func (t *Tools) link(_ context.Context, s *session, a any) (contract.ToolResult, error) {
	args := a.(*contract.LinkArgs)
	from, err := t.writeNode(s, "from_id", args.FromID, "")
	if err != nil {
		return contract.ToolResult{}, err
	}
	to, err := t.writeNode(s, "to_id", args.ToID, "")
	if err != nil {
		return contract.ToolResult{}, err
	}
	if from == to {
		return contract.ToolResult{}, contract.FieldErr(contract.ErrBadRequest, "to_id", "a link must join two different nodes")
	}
	lid := store.NewID(contract.PrefixLink)
	out, err := t.commit(s, []contract.Op{newOp(s.mapID, &contract.LinkCreatePayload{Link: contract.LinkInit{ID: lid, From: from, To: to, Label: args.Label, Reason: args.Reason}})}, false)
	if err != nil {
		return contract.ToolResult{}, err
	}
	out.NodeIDs = []string{from, to}
	if out.Applied {
		t.activity(s, contract.ActivityLinkAdded, "linked two nodes: "+args.Label, []string{from, to}, 1, out.Rev)
	}
	r := outcomeResult(fmt.Sprintf("linked [%s] -%s-> [%s] as %s", from, args.Label, to, lid), out)
	r.Structured["link_id"] = lid
	return r, nil
}

func (t *Tools) claim(_ context.Context, s *session, a any) (contract.ToolResult, error) {
	args := a.(*contract.ClaimArgs)
	id, err := t.writeNode(s, "node_id", args.NodeID, "")
	if err != nil {
		return contract.ToolResult{}, err
	}
	n, err := t.svc.Store.Node(s.mapID, id)
	if err != nil {
		return contract.ToolResult{}, contract.FieldErr(contract.ErrNotFound, "node_id", "node %s not found (claims need a live node)", id)
	}
	now := t.now()
	if c := n.Claim; c != nil && c.ParticipantID != s.actor.ParticipantID && now.Before(c.ExpiresAt) {
		who := c.ParticipantID
		if p, err := t.svc.Store.GetParticipant(c.ParticipantID); err == nil {
			who = p.Name
		}
		return contract.ToolResult{}, contract.FieldErr(contract.ErrClaimed, "node_id", "claimed by %s until %s; pick another node", who, c.ExpiresAt.Format(time.RFC3339))
	}
	var op contract.Op
	var text string
	typ := contract.ActivityClaimed
	if args.Action == contract.ClaimActionRelease {
		op, text, typ = newOp(s.mapID, &contract.NodeReleasePayload{ID: id}), fmt.Sprintf("released [%s]", id), contract.ActivityReleased
	} else {
		ttl := contract.DefaultClaimTTL
		if args.TTLMinutes > 0 {
			ttl = time.Duration(args.TTLMinutes) * time.Minute
		}
		exp := now.Add(ttl)
		op = newOp(s.mapID, &contract.NodeClaimPayload{ID: id, Claim: contract.Claim{ParticipantID: s.actor.ParticipantID, RunID: s.actor.RunID, Note: args.Note, ExpiresAt: exp}})
		text = fmt.Sprintf("claimed [%s] %s until %s (claim again to renew)", id, n.Title, exp.Format(time.RFC3339))
	}
	// Claims are coordination, not content: always live, even in propose mode.
	out, err := t.commit(s, []contract.Op{op}, true)
	if err != nil {
		return contract.ToolResult{}, err
	}
	out.NodeIDs = []string{id}
	t.focus(s, id)
	t.activity(s, typ, string(typ[5:])+" \""+n.Title+"\"", []string{id}, 0, out.Rev)
	return outcomeResult(text, out), nil
}

func (t *Tools) spawn(ctx context.Context, s *session, a any) (contract.ToolResult, error) {
	args := a.(*contract.SpawnArgs)
	if s.personal() {
		return contract.ToolResult{}, contract.Errf(contract.ErrForbidden, "spawn needs a run; a personal token has no budget to spend")
	}
	left := t.budget(s)
	if left != nil && left.Subagents >= 0 && len(args.Agents) > left.Subagents {
		return contract.ToolResult{}, contract.FieldErr(contract.ErrBudgetExceeded, "agents", "you asked for %d sub-agents but %d are left", len(args.Agents), left.Subagents)
	}
	if err := t.svc.CheckRunCaps(s.mapID, nil, len(args.Agents)); err != nil {
		return contract.ToolResult{}, err
	}
	nodes := make([]string, len(args.Agents))
	for i, ag := range args.Agents {
		id, err := t.writeNode(s, fmt.Sprintf("agents[%d].node_id", i), ag.NodeID, "")
		if err != nil {
			return contract.ToolResult{}, err
		}
		nodes[i] = id
	}
	var runs []map[string]any
	var lines []string
	for i, ag := range args.Agents {
		profile := ag.Profile
		if profile == "" {
			profile = s.run.ProfileID
		}
		mode := ag.Mode
		if mode == "" {
			mode = s.run.Mode
		}
		// The service resolves the profile's name and harness from Kandev;
		// these are the fallbacks when it cannot (no host).
		harness, name := contract.HarnessOther, ""
		if profile == s.run.ProfileID {
			harness, name = s.run.Harness, s.run.ProfileName
		}
		tmpl := ag.Template
		if tmpl == "" {
			tmpl = contract.TemplateResearch
		}
		r, err := t.svc.StartRun(ctx, app.RunSpec{NewRun: store.NewRun{MapID: s.mapID, NodeID: nodes[i], WorkspaceID: s.workspaceID, ProfileID: profile,
			ProfileName: name, Harness: harness, Template: tmpl, Lens: ag.Lens, Mode: mode, Goal: ag.Goal, Budget: ag.Budget,
			ParentRunID: s.run.ID, SwarmID: s.run.SwarmID, StartedBy: s.run.StartedBy}})
		if err != nil && r.ID == "" {
			if len(runs) > 0 {
				e := *contract.AsError(err)
				e.Message = fmt.Sprintf("%s (agents[0..%d] were spawned: %s)", e.Message, i-1, strings.Join(lines, "; "))
				return contract.ToolResult{}, &e
			}
			return contract.ToolResult{}, err
		}
		runs = append(runs, map[string]any{"run_id": r.ID, "node_id": r.NodeID, "task_id": r.TaskID, "status": r.Status})
		ln := fmt.Sprintf("%s on [%s] (%s)", r.ID, r.NodeID, r.Status)
		if r.Error != "" {
			ln += ": " + r.Error
		}
		lines = append(lines, ln)
	}
	text := "spawned:\n- " + strings.Join(lines, "\n- ")
	if bl := t.budget(s); bl != nil && bl.Subagents >= 0 {
		text += fmt.Sprintf("\nsub-agents left: %d", bl.Subagents)
	}
	return contract.ToolResult{Text: text, Structured: map[string]any{"runs": runs}}, nil
}

func (t *Tools) askHuman(_ context.Context, s *session, a any) (contract.ToolResult, error) {
	args := a.(*contract.AskHumanArgs)
	if s.personal() {
		return contract.ToolResult{}, contract.Errf(contract.ErrForbidden, "ask_human needs a run: the answer is sent back to the run's agent")
	}
	parent, err := t.writeNode(s, "node_id", args.NodeID, s.run.NodeID)
	if err != nil {
		return contract.ToolResult{}, err
	}
	ti, err := t.tree(s.mapID)
	if err != nil {
		return contract.ToolResult{}, err
	}
	var body []string
	if q := strings.TrimSpace(args.Question); len(q) > contract.MaxTitleLen || strings.Contains(q, "\n") {
		body = append(body, q)
	}
	if args.Context != "" {
		body = append(body, args.Context)
	}
	if len(args.Options) > 0 {
		var ol []string
		for _, o := range args.Options {
			ol = append(ol, "- "+o)
		}
		body = append(body, "Options:\n"+strings.Join(ol, "\n"))
	}
	id := store.NewID(contract.PrefixNode)
	op := newOp(s.mapID, &contract.NodeCreatePayload{Node: contract.NodeInit{ID: id, ParentID: parent, Order: contract.OrderBetween(ti.lastChild(parent), ""),
		Title: title(args.Question), BodyMD: strings.Join(body, "\n\n"), Kind: contract.KindQuestion,
		Question: &contract.Question{RunID: s.run.ID, Options: args.Options}}})
	// Questions are always live so a human can answer them.
	out, err := t.commit(s, []contract.Op{op}, true)
	if err != nil {
		return contract.ToolResult{}, err
	}
	_, _ = t.svc.UpdateRun(s.run.ID, func(r *store.RunRecord) bool {
		r.Status, r.WaitingReason = contract.RunWaiting, contract.WaitingQuestion
		return true
	})
	t.focus(s, id)
	t.activity(s, contract.ActivityQuestionAsked, "asked: "+title(args.Question), []string{id}, 1, out.Rev)
	return contract.ToolResult{
		Text:       fmt.Sprintf("asked as question node [%s]. Keep working on other parts if you can; the answer arrives as a chat message.", id),
		Structured: map[string]any{"node_id": id, "rev": out.Rev},
	}, nil
}

func (t *Tools) writeDoc(_ context.Context, s *session, a any) (contract.ToolResult, error) {
	args := a.(*contract.WriteDocArgs)
	var sources []string
	for i, x := range args.SourceIDs {
		id, err := t.nodeID(s, fmt.Sprintf("source_ids[%d]", i), x, "")
		if err != nil {
			return contract.ToolResult{}, err
		}
		sources = append(sources, id)
	}
	runID := ""
	if s.run != nil {
		runID = s.run.ID
	}
	meta := &contract.DocMeta{Path: args.Path, SourceIDs: sources, RunID: runID, CompiledAt: t.now()}
	var op contract.Op
	var id, verb string
	if args.DocID != "" {
		did, err := t.writeNode(s, "doc_id", args.DocID, "")
		if err != nil {
			return contract.ToolResult{}, err
		}
		if n, err := t.svc.Store.Node(s.mapID, did); err == nil {
			if n.Kind != contract.KindDoc {
				return contract.ToolResult{}, contract.FieldErr(contract.ErrBadRequest, "doc_id", "node %s is a %s, not a doc", did, n.Kind)
			}
			if len(meta.SourceIDs) == 0 && n.Doc != nil {
				meta.SourceIDs = n.Doc.SourceIDs // keep sources unless new ones are given
			}
		}
		ttl, body := args.Title, args.Body
		id, verb = did, "updated"
		op = newOp(s.mapID, &contract.NodeUpdatePayload{ID: did, Set: &contract.NodeFields{Title: &ttl, BodyMD: &body, Doc: meta}})
	} else {
		def := ""
		if s.run != nil {
			def = s.run.NodeID
		}
		parent, err := t.writeNode(s, "parent_doc_id", args.ParentDocID, def)
		if err != nil {
			return contract.ToolResult{}, err
		}
		ti, err := t.tree(s.mapID)
		if err != nil {
			return contract.ToolResult{}, err
		}
		id, verb = store.NewID(contract.PrefixNode), "wrote"
		op = newOp(s.mapID, &contract.NodeCreatePayload{Node: contract.NodeInit{ID: id, ParentID: parent, Order: contract.OrderBetween(ti.lastChild(parent), ""),
			Title: args.Title, BodyMD: args.Body, Kind: contract.KindDoc, Doc: meta}})
	}
	out, err := t.commit(s, []contract.Op{op}, false)
	if err != nil {
		return contract.ToolResult{}, err
	}
	if args.Ref != "" {
		t.setRefs(s, map[string]string{args.Ref: id})
		out.Refs = map[string]string{args.Ref: id}
	}
	out.NodeIDs = []string{id}
	t.focus(s, id)
	if out.Applied {
		t.activity(s, contract.ActivityDocWritten, verb+" doc "+args.Path, []string{id}, 1, out.Rev)
	}
	r := outcomeResult(fmt.Sprintf("%s doc [%s] %s", verb, id, args.Path), out)
	r.Structured["node_id"], r.Structured["path"] = id, args.Path
	return r, nil
}

func (t *Tools) report(_ context.Context, s *session, a any) (contract.ToolResult, error) {
	args := a.(*contract.ReportArgs)
	if s.personal() {
		return contract.ToolResult{}, contract.Errf(contract.ErrForbidden, "report needs a run")
	}
	nodeID, err := t.writeNode(s, "node_id", args.NodeID, s.run.NodeID)
	if err != nil {
		return contract.ToolResult{}, err
	}
	summary := args.Summary
	if len(args.NextSteps) > 0 {
		summary += "\n\nNext steps:\n- " + strings.Join(args.NextSteps, "\n- ")
	}
	name := "agent"
	if p, err := t.svc.Store.GetParticipant(s.actor.ParticipantID); err == nil {
		name = p.Name
	}
	section := "### Run summary (" + name + ")\n\n" + summary
	var rev int64
	if n, err := t.svc.Store.Node(s.mapID, nodeID); err == nil && !n.Deleted && len(n.BodyMD)+len(section) < contract.MaxBodyLen {
		res, err := t.svc.Submit(s.mapID, []contract.Op{newOp(s.mapID, &contract.NodeUpdatePayload{ID: nodeID, AppendBody: section})},
			appSubmit(s.actor))
		if err == nil {
			rev = res.Rev
		}
	}
	if s.run.ProposalID != "" {
		if err := t.svc.Store.SetProposalReady(s.run.ProposalID, args.Summary); err == nil {
			if p, err := t.svc.Store.GetProposal(s.run.ProposalID); err == nil {
				t.svc.Hub.Publish(s.mapID, contract.Message{Type: contract.MsgProposal, Payload: &contract.ProposalPayload{Proposal: p}})
			}
		}
	}
	run, err := t.svc.UpdateRun(s.run.ID, func(r *store.RunRecord) bool {
		r.Summary, r.Reported, r.ReportStatus = summary, true, args.Status
		// Runs with no Kandev session (bridge outside Kandev) never get a
		// settle event, so settle now.
		if r.TaskID == "" {
			open, _ := t.svc.Store.OpenQuestions(r.MapID, r.ID)
			if st, wr, ok := contract.NextRunStatus(t.facts(*r, open), contract.SessionIdle); ok {
				r.Status, r.WaitingReason = st, wr
			}
		}
		return true
	})
	if err != nil {
		return contract.ToolResult{}, err
	}
	t.svc.SetRunNodeStatus(run)
	t.activity(s, contract.ActivityRunStatus, "reported: "+title(args.Summary), []string{nodeID}, 0, rev)
	text := fmt.Sprintf("reported (%s). The run moves to %s when your turn ends; stop now unless a human asks for more.", args.Status, args.Status)
	return contract.ToolResult{Text: text, Structured: map[string]any{"run_id": run.ID, "status": run.Status, "rev": rev}}, nil
}

// Facts builds the NextRunStatus inputs of a run.
func (t *Tools) facts(r store.RunRecord, openQuestion bool) contract.RunFacts {
	return Facts(t.svc.Store, r, openQuestion)
}

// Facts builds the NextRunStatus inputs of a run.
func Facts(st *store.Store, r store.RunRecord, openQuestion bool) contract.RunFacts {
	f := contract.RunFacts{Status: r.Status, Reported: r.Reported, ReportStatus: r.ReportStatus, OpenQuestion: openQuestion, StopRequested: r.StopRequested}
	if r.ProposalID != "" {
		if p, err := st.GetProposal(r.ProposalID); err == nil && !p.Status.Resolved() {
			for _, it := range p.Items {
				if it.Status == contract.ItemPending {
					f.OpenProposal = true
					break
				}
			}
		}
	}
	return f
}
