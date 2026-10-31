package store

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"kandev-plugin-mindmap/server/contract"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newMap(t *testing.T, s *Store) (contract.Map, contract.Participant) {
	t.Helper()
	m, host, err := s.CreateMap(contract.CreateMapRequest{WorkspaceID: "ws-1", Title: "Pricing strategy"})
	if err != nil {
		t.Fatal(err)
	}
	return m, host
}

func createOp(mapID, id, parent, order, title string) contract.Op {
	return contract.Op{ID: NewID(contract.PrefixOp), MapID: mapID, Type: contract.OpNodeCreate, Payload: &contract.NodeCreatePayload{
		Node: contract.NodeInit{ID: id, ParentID: parent, Order: order, Title: title, Kind: contract.KindIdea}}}
}

func moveOp(mapID, id, parent, order string) contract.Op {
	return contract.Op{ID: NewID(contract.PrefixOp), MapID: mapID, Type: contract.OpNodeMove,
		Payload: &contract.NodeMovePayload{ID: id, ParentID: parent, Order: order}}
}

func setTitle(mapID, id, title string) contract.Op {
	return contract.Op{ID: NewID(contract.PrefixOp), MapID: mapID, Type: contract.OpNodeUpdate,
		Payload: &contract.NodeUpdatePayload{ID: id, Set: &contract.NodeFields{Title: &title}}}
}

func code(err error) contract.ErrorCode {
	if err == nil {
		return ""
	}
	return contract.AsError(err).Code
}

func TestCreateMapAndSnapshot(t *testing.T) {
	s := openTest(t)
	m, host := newMap(t, s)
	if m.Rev != 1 || !host.IsHost {
		t.Fatalf("map=%+v host=%+v", m, host)
	}
	snap, err := s.Snapshot(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Nodes) != 1 || snap.Nodes[0].ID != m.RootID || snap.Nodes[0].Kind != contract.KindTopic {
		t.Fatalf("snapshot nodes %+v", snap.Nodes)
	}
	if err := contract.ValidateTree(snap.Nodes); err != nil {
		t.Fatal(err)
	}
	// State survives a reopen (cache dropped).
	s2, err := Open(s.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	snap2, err := s2.Snapshot(m.ID)
	if err != nil || snap2.Rev != 1 || len(snap2.Nodes) != 1 {
		t.Fatalf("reopen snapshot %+v err=%v", snap2, err)
	}
}

func TestSubmitBatchAtomicAndIdempotent(t *testing.T) {
	s := openTest(t)
	m, host := newMap(t, s)
	actor := SubmitOptions{Actor: contract.Actor{ParticipantID: host.ID}}
	a, b := NewID("nd"), NewID("nd")
	ops := []contract.Op{createOp(m.ID, a, m.RootID, "V", "A"), createOp(m.ID, b, a, "V", "B")}
	res, err := s.Submit(m.ID, ops, actor)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Acks) != 2 || res.Acks[0].Rev != 2 || res.Acks[1].Rev != 3 || res.Rev != 3 {
		t.Fatalf("acks %+v rev %d", res.Acks, res.Rev)
	}
	// Re-submit: acked with original revs, not re-applied.
	res2, err := s.Submit(m.ID, ops, actor)
	if err != nil || len(res2.Committed) != 0 || res2.Acks[1].Rev != 3 {
		t.Fatalf("resubmit %+v err=%v", res2, err)
	}
	// A batch whose second op is a cycle rejects whole; the first op does not land.
	c := NewID("nd")
	_, err = s.Submit(m.ID, []contract.Op{createOp(m.ID, c, m.RootID, "W", "C"), moveOp(m.ID, a, b, "V")}, actor)
	if code(err) != contract.ErrCycle || contract.AsError(err).Field != "ops[1].payload.parent_id" {
		t.Fatalf("want cycle on ops[1], got %v", err)
	}
	if _, err := s.Node(m.ID, c); code(err) != contract.ErrNotFound {
		t.Fatalf("rolled-back create is visible: %v", err)
	}
	if mm, _ := s.GetMap(m.ID); mm.Rev != 3 {
		t.Fatalf("rev moved on reject: %d", mm.Rev)
	}
	ops2, _ := s.OpsSince(m.ID, 0, 0)
	if len(ops2) != 3 {
		t.Fatalf("op log has %d entries", len(ops2))
	}
}

func TestOrderCollisionRebases(t *testing.T) {
	s := openTest(t)
	m, host := newMap(t, s)
	opt := SubmitOptions{Actor: contract.Actor{ParticipantID: host.ID}}
	a, b, c := NewID("nd"), NewID("nd"), NewID("nd")
	if _, err := s.Submit(m.ID, []contract.Op{createOp(m.ID, a, m.RootID, "V", "A"), createOp(m.ID, c, m.RootID, "X", "C")}, opt); err != nil {
		t.Fatal(err)
	}
	res, err := s.Submit(m.ID, []contract.Op{createOp(m.ID, b, m.RootID, "V", "B")}, opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rebased) != 1 {
		t.Fatalf("want a rebase, got %+v", res)
	}
	got := res.Rebased[0].Payload.(*contract.NodeCreatePayload).Node.Order
	if !(got > "V" && got < "X") {
		t.Fatalf("rebased order %q not between V and X", got)
	}
	n, _ := s.Node(m.ID, b)
	if n.Order != got {
		t.Fatalf("stored order %q != rebased %q", n.Order, got)
	}
}

func TestFieldLWWAndMergeStyleFields(t *testing.T) {
	s := openTest(t)
	m, host := newMap(t, s)
	opt := SubmitOptions{Actor: contract.Actor{ParticipantID: host.ID}}
	id := NewID("nd")
	if _, err := s.Submit(m.ID, []contract.Op{createOp(m.ID, id, m.RootID, "V", "Old")}, opt); err != nil {
		t.Fatal(err)
	}
	body := "Body"
	up := func(p *contract.NodeUpdatePayload) contract.Op {
		p.ID = id
		return contract.Op{ID: NewID("op"), MapID: m.ID, Type: contract.OpNodeUpdate, Payload: p}
	}
	ops := []contract.Op{
		setTitle(m.ID, id, "First"),
		up(&contract.NodeUpdatePayload{Set: &contract.NodeFields{BodyMD: &body}}),
		setTitle(m.ID, id, "Second"),
		up(&contract.NodeUpdatePayload{AppendBody: "More"}),
		up(&contract.NodeUpdatePayload{AddTags: []string{"a", "b"}}),
		up(&contract.NodeUpdatePayload{AddTags: []string{"b", "c"}, RemoveTags: []string{"a"}}),
		up(&contract.NodeUpdatePayload{AddSources: []contract.Source{{URL: "https://example.com/a"}}}),
		up(&contract.NodeUpdatePayload{AddSources: []contract.Source{{URL: "https://example.com/a"}, {URL: "https://example.com/b"}}}),
	}
	for _, o := range ops {
		if _, err := s.Submit(m.ID, []contract.Op{o}, opt); err != nil {
			t.Fatal(err)
		}
	}
	n, _ := s.Node(m.ID, id)
	if n.Title != "Second" || n.BodyMD != "Body\n\nMore" || fmt.Sprint(n.Tags) != "[b c]" || len(n.Sources) != 2 {
		t.Fatalf("node %+v", n)
	}
}

func TestSoftLockRejectsOthersOnly(t *testing.T) {
	s := openTest(t)
	m, host := newMap(t, s)
	id := NewID("nd")
	if _, err := s.Submit(m.ID, []contract.Op{createOp(m.ID, id, m.RootID, "V", "T")}, SubmitOptions{Actor: contract.Actor{ParticipantID: host.ID}}); err != nil {
		t.Fatal(err)
	}
	locked := func(node string, f contract.LockField, by string) bool { return node == id && f == contract.LockTitle && by != host.ID }
	_, err := s.Submit(m.ID, []contract.Op{setTitle(m.ID, id, "X")}, SubmitOptions{Actor: contract.Actor{ParticipantID: "pt_other00000001"}, Locked: locked})
	if code(err) != contract.ErrLocked {
		t.Fatalf("want locked, got %v", err)
	}
	if _, err := s.Submit(m.ID, []contract.Op{setTitle(m.ID, id, "Y")}, SubmitOptions{Actor: contract.Actor{ParticipantID: host.ID}, Locked: locked}); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteRestoreAndDeletedTargets(t *testing.T) {
	s := openTest(t)
	m, host := newMap(t, s)
	opt := SubmitOptions{Actor: contract.Actor{ParticipantID: host.ID}}
	a, b := NewID("nd"), NewID("nd")
	if _, err := s.Submit(m.ID, []contract.Op{createOp(m.ID, a, m.RootID, "V", "A"), createOp(m.ID, b, a, "V", "B")}, opt); err != nil {
		t.Fatal(err)
	}
	del := contract.Op{ID: NewID("op"), MapID: m.ID, Type: contract.OpNodeDelete, Payload: &contract.NodeDeletePayload{ID: a}}
	if _, err := s.Submit(m.ID, []contract.Op{del}, opt); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Node(m.ID, b); !n.Deleted {
		t.Fatal("child not tombstoned")
	}
	if _, err := s.Submit(m.ID, []contract.Op{setTitle(m.ID, b, "X")}, opt); code(err) != contract.ErrDeleted {
		t.Fatalf("update of tombstone: %v", err)
	}
	if _, err := s.Submit(m.ID, []contract.Op{createOp(m.ID, NewID("nd"), a, "V", "Z")}, opt); code(err) != contract.ErrDeleted {
		t.Fatalf("create under tombstone: %v", err)
	}
	res, err := s.Undo(m.ID, host.ID, opt)
	if err != nil || len(res.Committed) != 1 || res.Committed[0].Type != contract.OpNodeRestore {
		t.Fatalf("undo %+v err=%v", res, err)
	}
	if n, _ := s.Node(m.ID, b); n.Deleted {
		t.Fatal("child not restored")
	}
	// The next undo targets the create batch (the delete is already undone).
	res, err = s.Undo(m.ID, host.ID, opt)
	if err != nil || len(res.Committed) != 2 {
		t.Fatalf("second undo %+v err=%v", res, err)
	}
	if n, _ := s.Node(m.ID, a); !n.Deleted {
		t.Fatal("create not undone")
	}
}

func TestUndoIsPerAuthor(t *testing.T) {
	s := openTest(t)
	m, host := newMap(t, s)
	alice := contract.Actor{ParticipantID: host.ID}
	bob := contract.Actor{ParticipantID: "pt_bob000000001"}
	id := NewID("nd")
	if _, err := s.Submit(m.ID, []contract.Op{createOp(m.ID, id, m.RootID, "V", "Start")}, SubmitOptions{Actor: alice}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(m.ID, []contract.Op{setTitle(m.ID, id, "Alice")}, SubmitOptions{Actor: alice}); err != nil {
		t.Fatal(err)
	}
	color := "#ff0000"
	bobOp := contract.Op{ID: NewID("op"), MapID: m.ID, Type: contract.OpNodeUpdate, Payload: &contract.NodeUpdatePayload{ID: id, Set: &contract.NodeFields{Color: &color}}}
	if _, err := s.Submit(m.ID, []contract.Op{bobOp}, SubmitOptions{Actor: bob}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Undo(m.ID, alice.ParticipantID, SubmitOptions{Actor: alice}); err != nil {
		t.Fatal(err)
	}
	n, _ := s.Node(m.ID, id)
	if n.Title != "Start" || n.Color != color {
		t.Fatalf("undo clobbered bob or missed alice: %+v", n)
	}
}

func TestMergeRewiresChildrenAndLinks(t *testing.T) {
	s := openTest(t)
	m, host := newMap(t, s)
	opt := SubmitOptions{Actor: contract.Actor{ParticipantID: host.ID}}
	a, b, bc, d := NewID("nd"), NewID("nd"), NewID("nd"), NewID("nd")
	link := contract.Op{ID: NewID("op"), MapID: m.ID, Type: contract.OpLinkCreate, Payload: &contract.LinkCreatePayload{
		Link: contract.LinkInit{ID: NewID("ln"), From: b, To: d, Label: "supports"}}}
	ops := []contract.Op{createOp(m.ID, a, m.RootID, "V", "A"), createOp(m.ID, b, m.RootID, "W", "B"), createOp(m.ID, bc, b, "V", "B child"),
		createOp(m.ID, d, m.RootID, "X", "D"), link}
	if _, err := s.Submit(m.ID, ops, opt); err != nil {
		t.Fatal(err)
	}
	merge := contract.Op{ID: NewID("op"), MapID: m.ID, Type: contract.OpNodeMerge, Payload: &contract.NodeMergePayload{IntoID: a, IDs: []string{b}}}
	if _, err := s.Submit(m.ID, []contract.Op{merge}, opt); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(m.ID)
	if err := contract.ValidateTree(snap.Nodes); err != nil {
		t.Fatal(err)
	}
	child, _ := s.Node(m.ID, bc)
	into, _ := s.Node(m.ID, a)
	if child.ParentID != a || into.BodyMD != "## B" || len(snap.Links) != 1 || snap.Links[0].From != a {
		t.Fatalf("child=%+v into=%q links=%+v", child, into.BodyMD, snap.Links)
	}
	// Merging an ancestor into its own descendant is a cycle.
	bad := contract.Op{ID: NewID("op"), MapID: m.ID, Type: contract.OpNodeMerge, Payload: &contract.NodeMergePayload{IntoID: bc, IDs: []string{a}}}
	if _, err := s.Submit(m.ID, []contract.Op{bad}, opt); code(err) != contract.ErrCycle {
		t.Fatalf("want cycle, got %v", err)
	}
}

func TestProposalAcceptPartialAndConflict(t *testing.T) {
	s := openTest(t)
	m, host := newMap(t, s)
	run, _, err := s.CreateRun(NewRun{MapID: m.ID, NodeID: m.RootID, Template: contract.TemplateGarden, StartedBy: host.ID}, 4242)
	if err != nil {
		t.Fatal(err)
	}
	if run.Mode != contract.ModePropose || run.ProposalID == "" {
		t.Fatalf("garden run should propose: %+v", run.Run)
	}
	agent := contract.Actor{ParticipantID: run.ParticipantID, RunID: run.ID}
	a, b, c := NewID("nd"), NewID("nd"), NewID("nd")
	// b is a child of ghost a: valid only because a is pending in the same proposal.
	if _, err := s.Propose(run.ProposalID, []contract.Op{createOp(m.ID, a, m.RootID, "V", "Ghost A")}, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Propose(run.ProposalID, []contract.Op{createOp(m.ID, b, a, "V", "Ghost B"), createOp(m.ID, c, m.RootID, "W", "Ghost C")}, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Propose(run.ProposalID, []contract.Op{createOp(m.ID, NewID("nd"), "nd_missing00000001", "V", "X")}, agent); code(err) != contract.ErrNotFound {
		t.Fatalf("invalid proposed op accepted: %v", err)
	}
	if mm, _ := s.GetMap(m.ID); mm.Rev != 1 {
		t.Fatalf("propose applied ops: rev %d", mm.Rev)
	}
	p, _ := s.GetProposal(run.ProposalID)
	if len(p.Items) != 3 {
		t.Fatalf("items %+v", p.Items)
	}
	// Reject a, accept b and c: b conflicts (depends on rejected a), c applies.
	res, err := s.ReviewProposal(p.ID, contract.ReviewProposalRequest{Reject: []string{p.Items[0].Op.ID}, Accept: []string{p.Items[1].Op.ID, p.Items[2].Op.ID}}, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	st := []contract.ProposalItemStatus{res.Proposal.Items[0].Status, res.Proposal.Items[1].Status, res.Proposal.Items[2].Status}
	if fmt.Sprint(st) != "[rejected conflict accepted]" || res.Proposal.Status != contract.ProposalPartial || len(res.Committed) != 1 {
		t.Fatalf("statuses %v proposal %s committed %d", st, res.Proposal.Status, len(res.Committed))
	}
	if res.Committed[0].ProposalID != p.ID || res.Committed[0].Actor.RunID != run.ID {
		t.Fatalf("applied op attribution %+v", res.Committed[0])
	}
	if _, err := s.ReviewProposal(p.ID, contract.ReviewProposalRequest{AcceptAll: true}, host.ID); code(err) != contract.ErrProposalClosed {
		t.Fatalf("closed proposal reviewed again: %v", err)
	}
}

func TestForkMapAtRev(t *testing.T) {
	s := openTest(t)
	m, host := newMap(t, s)
	opt := SubmitOptions{Actor: contract.Actor{ParticipantID: host.ID}}
	a := NewID("nd")
	if _, err := s.Submit(m.ID, []contract.Op{createOp(m.ID, a, m.RootID, "V", "A")}, opt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(m.ID, []contract.Op{setTitle(m.ID, a, "A2"), createOp(m.ID, NewID("nd"), a, "V", "A child")}, opt); err != nil {
		t.Fatal(err)
	}
	f, err := s.ForkMap(m.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(f.ID)
	if f.ForkedFrom == nil || f.ForkedFrom.Rev != 2 || len(snap.Nodes) != 2 {
		t.Fatalf("fork %+v nodes %d", f, len(snap.Nodes))
	}
	for _, n := range snap.Nodes {
		if n.ID != f.RootID && n.Title != "A" {
			t.Fatalf("fork at rev 2 should have the old title: %q", n.Title)
		}
	}
	head, err := s.ForkMap(m.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if snap, _ := s.Snapshot(head.ID); len(snap.Nodes) != 3 {
		t.Fatalf("head fork has %d nodes", len(snap.Nodes))
	}
}

func TestTokensHashedAndScoped(t *testing.T) {
	s := openTest(t)
	m, host := newMap(t, s)
	raw, info, err := s.IssueToken(contract.TokenLocal, 0, m.ID, "", host.ID, "", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.LookupToken(raw)
	if err != nil || got.ID != info.ID || got.MapID != m.ID {
		t.Fatalf("lookup %+v err=%v", got, err)
	}
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM tokens WHERE hash = ?`, raw).Scan(&n)
	if n != 0 {
		t.Fatal("raw token stored")
	}
	if _, err := s.LookupToken(raw[:len(raw)-1] + "A"); err == nil {
		t.Fatal("tampered token accepted")
	}
	_ = s.RevokeToken(info.ID)
	if _, err := s.LookupToken(raw); err == nil {
		t.Fatal("revoked token accepted")
	}
	s.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	raw2, _, _ := s.IssueToken(contract.TokenLocal, 0, m.ID, "", host.ID, "", "", time.Minute)
	s.Now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	if _, err := s.LookupToken(raw2); err == nil {
		t.Fatal("expired token accepted")
	}
}

// Two human writers and an agent hammer one map concurrently. Every op must
// commit exactly once with a dense rev sequence and a valid tree.
func TestConcurrentWritersAndAgent(t *testing.T) {
	s := openTest(t)
	m, host := newMap(t, s)
	guest, err := s.CreateGuest(m.ID, "Guest", "#00ff00")
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := s.CreateRun(NewRun{MapID: m.ID, NodeID: m.RootID, StartedBy: host.ID}, 4242)
	if err != nil {
		t.Fatal(err)
	}
	actors := []contract.Actor{{ParticipantID: host.ID}, {ParticipantID: guest.ID}, {ParticipantID: run.ParticipantID, RunID: run.ID}}
	const per = 40
	var wg sync.WaitGroup
	errs := make(chan error, len(actors)*per)
	for w, actor := range actors {
		wg.Add(1)
		go func(w int, actor contract.Actor) {
			defer wg.Done()
			var mine []string
			for i := 0; i < per; i++ {
				id := NewID("nd")
				parent := m.RootID
				if len(mine) > 0 && i%3 == 0 {
					parent = mine[len(mine)-1]
				}
				ops := []contract.Op{createOp(m.ID, id, parent, "V", fmt.Sprintf("w%d-%d", w, i))}
				if len(mine) > 1 && i%5 == 0 {
					// Try to move an earlier node somewhere: may legitimately fail with cycle.
					ops = append(ops, moveOp(m.ID, mine[0], mine[len(mine)-1], "V"))
				}
				if _, err := s.Submit(m.ID, ops, SubmitOptions{Actor: actor}); err != nil && code(err) != contract.ErrCycle {
					errs <- err
				}
				mine = append(mine, id)
				if i%7 == 0 {
					_, _ = s.Snapshot(m.ID)
				}
			}
		}(w, actor)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	ops, err := s.OpsSince(m.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, o := range ops {
		if o.Rev != int64(i+1) {
			t.Fatalf("rev gap at %d: %d", i, o.Rev)
		}
	}
	snap, _ := s.Snapshot(m.ID)
	if err := contract.ValidateTree(snap.Nodes); err != nil {
		t.Fatal(err)
	}
	if snap.Rev != int64(len(ops)) {
		t.Fatalf("map rev %d, log %d", snap.Rev, len(ops))
	}
	// Siblings never share an order key after rebases.
	seen := map[string]bool{}
	for _, n := range snap.Nodes {
		k := n.ParentID + "/" + n.Order
		if seen[k] {
			t.Fatalf("duplicate order key %s", k)
		}
		seen[k] = true
	}
}

// Load: build a 1,000-node map via ops in under a second.
func TestLoadThousandNodes(t *testing.T) {
	s := openTest(t)
	m, host := newMap(t, s)
	opt := SubmitOptions{Actor: contract.Actor{ParticipantID: host.ID}}
	start := time.Now()
	parents := []string{m.RootID}
	var batch []contract.Op
	last := map[string]string{}
	for i := 0; i < 1000; i++ {
		parent := parents[i/10]
		id := NewID("nd")
		order := contract.OrderBetween(last[parent], "")
		last[parent] = order
		batch = append(batch, createOp(m.ID, id, parent, order, fmt.Sprintf("Idea %d", i)))
		parents = append(parents, id)
		if len(batch) == 50 {
			if _, err := s.Submit(m.ID, batch, opt); err != nil {
				t.Fatal(err)
			}
			batch = nil
		}
	}
	el := time.Since(start)
	snap, err := s.Snapshot(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Nodes) != 1001 {
		t.Fatalf("nodes %d", len(snap.Nodes))
	}
	t.Logf("1000 nodes in %v", el)
	if el > time.Second && !raceEnabled {
		t.Fatalf("1000 nodes took %v (want < 1s)", el)
	}
}
