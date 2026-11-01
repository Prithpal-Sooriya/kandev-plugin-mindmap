package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/store"
)

// Compile is a map-reduce over a branch: one section agent per top-level
// child, running in parallel as child runs of a compile coordinator, then
// one synthesis agent that writes the overview index and the consolidated
// doc. The backend lays the doc tree out first (placeholder doc nodes), so
// every agent is told the doc ids and paths it writes.

const (
	// MaxCompileSections caps the top-level branches of one compile.
	MaxCompileSections = 24
	// MaxCompileParallel is how many section agents run at once; the rest
	// start as earlier ones finish.
	MaxCompileParallel = 8
	// A branch splits into sub-docs at SplitChildren children or more than
	// SplitWords words.
	SplitChildren = 8
	SplitWords    = 1500
)

// CompileSectionBudget and CompileSynthesisBudget are the per-agent budgets.
var (
	CompileSectionBudget   = contract.Budget{MaxDepth: 3, MaxNodes: 20, MaxSubagents: 0, MaxMinutes: 20}
	CompileSynthesisBudget = contract.Budget{MaxDepth: 2, MaxNodes: 10, MaxSubagents: 0, MaxMinutes: 20}
)

// CompileSpec starts a compile of NodeID (default: the root).
type CompileSpec struct {
	MapID, NodeID string
	ProfileID     string
	Slug          string
	Target        string // "repo" or "dir"; default from the repository
	RepoID        string
	Mode          contract.RunMode
	StartedBy     string
}

// SectionPlan is one top-level branch of a compile.
type SectionPlan struct {
	NodeID   string      `json:"node_id"`
	Title    string      `json:"title"`
	Dir      string      `json:"dir"` // folder under <slug>/
	Words    int         `json:"words"`
	Split    bool        `json:"split"`
	Children []ChildPlan `json:"children,omitempty"`
	DocID    string      `json:"doc_id,omitempty"`
	RunID    string      `json:"run_id,omitempty"`
	Error    string      `json:"error,omitempty"` // the section agent could not start
}

// ChildPlan is a direct child of a section: the sub-doc it gets when the
// section splits.
type ChildPlan struct {
	NodeID string `json:"node_id"`
	Title  string `json:"title"`
	File   string `json:"file"`             // <child>.md inside the section folder
	DocID  string `json:"doc_id,omitempty"` // its sub-doc from an earlier compile
}

// compilePlan is a compile's layout and progress, kept in kv under
// "compile:<coordinator id>" so it survives restarts and approval.
type compilePlan struct {
	MapID       string           `json:"map_id"`
	NodeID      string           `json:"node_id"`
	Title       string           `json:"title"`
	Slug        string           `json:"slug"`
	Target      string           `json:"target"`
	RepoID      string           `json:"repo_id,omitempty"`
	ProfileID   string           `json:"profile_id"`
	ProfileName string           `json:"profile_name,omitempty"`
	Harness     contract.Harness `json:"harness,omitempty"`
	StartedBy   string           `json:"started_by"`
	Section     contract.Budget  `json:"section_budget"`
	DocRootID   string           `json:"doc_root_id"`
	SingleDocID string           `json:"single_doc_id"`
	Sections    []SectionPlan    `json:"sections"`
	SynthRunID  string           `json:"synthesis_run_id,omitempty"`
	SynthErr    string           `json:"synthesis_error,omitempty"`
	Exported    bool             `json:"exported,omitempty"`
}

// docRecord is what the backend knows about a compiled doc tree, under
// "docs:<doc root id>".
type docRecord struct {
	Slug        string `json:"slug"`
	SingleDocID string `json:"single_doc_id,omitempty"`
	CoordRunID  string `json:"coordinator_run_id,omitempty"`
	Target      string `json:"target,omitempty"`
	ExportedTo  string `json:"exported_to,omitempty"`
}

func planKey(coordID string) string  { return "compile:" + coordID }
func docKey(docRootID string) string { return "docs:" + docRootID }

func (s *Service) loadPlan(coordID string) (compilePlan, bool) {
	var p compilePlan
	v, ok, err := s.Store.GetKV(planKey(coordID))
	if err != nil || !ok || json.Unmarshal([]byte(v), &p) != nil {
		return p, false
	}
	return p, true
}

func (s *Service) savePlan(coordID string, p compilePlan) {
	b, _ := json.Marshal(p)
	if err := s.Store.SetKV(planKey(coordID), string(b)); err != nil {
		s.Logf("mindmap: save compile plan %s: %v", coordID, err)
	}
}

func (s *Service) loadDocRecord(docRootID string) (docRecord, bool) {
	var r docRecord
	v, ok, err := s.Store.GetKV(docKey(docRootID))
	if err != nil || !ok || json.Unmarshal([]byte(v), &r) != nil {
		return r, false
	}
	return r, true
}

func (s *Service) saveDocRecord(docRootID string, r docRecord) {
	b, _ := json.Marshal(r)
	if err := s.Store.SetKV(docKey(docRootID), string(b)); err != nil {
		s.Logf("mindmap: save doc record %s: %v", docRootID, err)
	}
}

// Slugify turns a title into a file-name slug: lower-case ASCII letters and
// digits joined by single hyphens, at most 48 characters.
func Slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 48 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// uniqueSlug returns base (or "section" when empty), suffixed -2, -3, ...
// until it is not taken; it marks the result taken.
func uniqueSlug(base string, taken map[string]bool) string {
	if base == "" {
		base = "section"
	}
	out := base
	for i := 2; taken[out]; i++ {
		out = fmt.Sprintf("%s-%d", base, i)
	}
	taken[out] = true
	return out
}

// childIndex lists each node's live children in sibling order.
func childIndex(nodes map[string]*contract.Node) map[string][]*contract.Node {
	kids := map[string][]*contract.Node{}
	for _, n := range nodes {
		if !n.Deleted && n.ParentID != "" {
			kids[n.ParentID] = append(kids[n.ParentID], n)
		}
	}
	for _, ks := range kids {
		slices.SortFunc(ks, func(a, b *contract.Node) int {
			if a.Order != b.Order {
				return strings.Compare(a.Order, b.Order)
			}
			return strings.Compare(a.ID, b.ID)
		})
	}
	return kids
}

func countWords(s string) int { return len(strings.Fields(s)) }

// PlanSections lays out the sections of a compile of rootID: one per live,
// non-doc child (or the node itself when it has none). A section splits
// into sub-docs at SplitChildren children or more than SplitWords words in
// its subtree.
func PlanSections(nodes map[string]*contract.Node, rootID string) []SectionPlan {
	kids := childIndex(nodes)
	content := func(n *contract.Node) []*contract.Node {
		var out []*contract.Node
		for _, c := range kids[n.ID] {
			if c.Kind != contract.KindDoc {
				out = append(out, c)
			}
		}
		return out
	}
	var words func(n *contract.Node, depth int) int
	words = func(n *contract.Node, depth int) int {
		w := countWords(n.Title) + countWords(n.BodyMD)
		if depth < 64 {
			for _, c := range content(n) {
				w += words(c, depth+1)
			}
		}
		return w
	}
	root := nodes[rootID]
	if root == nil {
		return nil
	}
	branches := content(root)
	if len(branches) == 0 {
		branches = []*contract.Node{root}
	}
	dirs := map[string]bool{"assets": true}
	out := make([]SectionPlan, 0, len(branches))
	for _, b := range branches {
		sec := SectionPlan{NodeID: b.ID, Title: b.Title, Dir: uniqueSlug(Slugify(b.Title), dirs), Words: words(b, 0)}
		files := map[string]bool{"index": true}
		for _, c := range content(b) {
			sec.Children = append(sec.Children, ChildPlan{NodeID: c.ID, Title: c.Title, File: uniqueSlug(Slugify(c.Title), files) + ".md"})
		}
		sec.Split = len(sec.Children) >= SplitChildren || sec.Words > SplitWords
		out = append(out, sec)
	}
	return out
}

// settled reports whether a child run has finished its part: ended, in
// review, or idle after its turn.
func settled(r store.RunRecord) bool {
	return r.Status.Terminal() || r.Status == contract.RunReview || r.Status == contract.RunWaiting && r.WaitingReason == contract.WaitingTurnEnded
}

// succeeded reports whether a settled run produced something to build on.
func succeeded(r store.RunRecord) bool {
	return settled(r) && r.Status != contract.RunFailed && r.Status != contract.RunStopped
}

// StartCompile creates the compile coordinator (no task), the placeholder
// doc tree under the node, and the first wave of section runs. The
// synthesis run starts once every section has settled. It returns the
// coordinator first, then the section runs. A guest's compile on a map that
// requires approval holds its section runs in waiting(approval); approving
// the coordinator launches them.
func (s *Service) StartCompile(ctx context.Context, spec CompileSpec) ([]store.RunRecord, error) {
	m, err := s.Store.GetMap(spec.MapID)
	if err != nil {
		return nil, err
	}
	if spec.NodeID == "" {
		spec.NodeID = m.RootID
	}
	node, err := s.Store.Node(spec.MapID, spec.NodeID)
	if err != nil || node.Deleted {
		return nil, contract.FieldErr(contract.ErrNotFound, "node_id", "node %s not found", spec.NodeID)
	}
	if node.Kind == contract.KindDoc {
		return nil, contract.FieldErr(contract.ErrBadRequest, "node_id", "compile a branch of the map, not a doc")
	}
	if spec.Mode != "" && !spec.Mode.Valid() {
		return nil, contract.FieldErr(contract.ErrBadRequest, "mode", "must be apply or propose")
	}
	repo := spec.RepoID
	if repo == "" {
		repo = m.RepoID
	}
	switch spec.Target {
	case "":
		spec.Target = "dir"
		if repo != "" {
			spec.Target = "repo"
		}
	case "repo":
		if repo == "" {
			return nil, contract.FieldErr(contract.ErrBadRequest, "target", "this map has no repository: compile to a folder (target dir)")
		}
	case "dir":
		repo = ""
	default:
		return nil, contract.FieldErr(contract.ErrBadRequest, "target", "must be repo or dir")
	}
	if spec.Slug != "" && Slugify(spec.Slug) == "" {
		return nil, contract.FieldErr(contract.ErrBadRequest, "slug", "needs at least one letter or digit")
	}
	nr := store.NewRun{ProfileID: spec.ProfileID}
	if nr.ProfileID == "" {
		nr.ProfileID = m.Settings.DefaultProfileID
	}
	if err := s.resolveProfile(ctx, &nr); err != nil {
		return nil, err
	}
	var sections []SectionPlan
	_ = s.Store.View(spec.MapID, func(_ contract.Map, nodes map[string]*contract.Node, _ map[string]*contract.Link) error {
		sections = PlanSections(nodes, spec.NodeID)
		return nil
	})
	if len(sections) > MaxCompileSections {
		return nil, contract.FieldErr(contract.ErrTooLarge, "node_id", "this branch has %d top-level branches; compile at most %d at once (compile a sub-branch)",
			len(sections), MaxCompileSections)
	}
	slug := Slugify(spec.Slug)
	if slug == "" {
		slug = Slugify(node.Title)
	}
	if slug == "" {
		slug = "mindmap"
	}
	prevRoot, prevRec := s.previousDocs(spec.MapID, spec.NodeID, slug)
	if prevRec.CoordRunID != "" {
		if c, err := s.Store.GetRun(prevRec.CoordRunID); err == nil && !c.Status.Terminal() {
			return nil, contract.Errf(contract.ErrConflict, "a compile of this branch into %q is still running (%s); wait for it or stop it", slug, c.ID)
		}
	}

	per := CompileSectionBudget
	clamped, err := s.clampGuestBudget(store.NewRun{MapID: spec.MapID, StartedBy: spec.StartedBy, Budget: &per})
	if err != nil {
		return nil, err
	}
	per = *clamped.Budget
	hold, err := s.needsApproval(store.NewRun{MapID: spec.MapID, StartedBy: spec.StartedBy})
	if err != nil {
		return nil, err
	}
	// The first wave fits in the free run slots and is reserved up front;
	// the rest start as section runs end. With no slot free, the
	// reservation of the one refuses the compile.
	free, err := s.freeRunSlots(spec.MapID)
	if err != nil {
		return nil, err
	}
	first := max(1, min(len(sections), MaxCompileParallel, free))
	var slots *runSlots
	if !hold {
		slots, err = s.reserveRunSlots(spec.MapID, &per, first)
		if err != nil {
			return nil, err
		}
		defer slots.release()
	}
	n := len(sections)
	synth := CompileSynthesisBudget
	pool := contract.Budget{MaxDepth: max(per.MaxDepth, synth.MaxDepth) + 1, MaxNodes: per.MaxNodes*n + synth.MaxNodes, MaxSubagents: n + 1,
		MaxMinutes: max(per.MaxMinutes, synth.MaxMinutes)}
	coord, _, err := s.Store.CreateRun(store.NewRun{MapID: spec.MapID, NodeID: spec.NodeID, Template: contract.TemplateCompile, Mode: spec.Mode,
		Goal: fmt.Sprintf("Compile \"%s\" into docs (%s)", node.Title, slug), Budget: &pool, Harness: contract.HarnessOther,
		ProfileName: fmt.Sprintf("%d section%s", n, plural(n)), Transport: contract.TransportMCP, StartedBy: spec.StartedBy}, s.Port())
	if err != nil {
		return nil, err
	}
	_ = s.Store.RevokeRunTokens(coord.ID) // no agent behind the coordinator
	coord, _ = s.UpdateRun(coord.ID, func(r *store.RunRecord) bool {
		r.Status = contract.RunRunning
		return true
	})
	s.announceRun(coord, node)

	plan := compilePlan{MapID: spec.MapID, NodeID: spec.NodeID, Title: node.Title, Slug: slug, Target: spec.Target, RepoID: repo,
		ProfileID: nr.ProfileID, ProfileName: nr.ProfileName, Harness: nr.Harness, StartedBy: spec.StartedBy, Section: per, Sections: sections,
		DocRootID: prevRoot}
	if err := s.addDocPlaceholders(&plan, node, prevRec); err != nil {
		slots.release()
		coord, _ = s.UpdateRun(coord.ID, func(r *store.RunRecord) bool {
			r.Status, r.Error = contract.RunFailed, "could not create the doc nodes: "+err.Error()
			return true
		})
		s.SetRunNodeStatus(coord)
		return []store.RunRecord{coord}, err
	}
	s.savePlan(coord.ID, plan)
	s.saveDocRecord(plan.DocRootID, docRecord{Slug: slug, SingleDocID: plan.SingleDocID, CoordRunID: coord.ID, Target: spec.Target, ExportedTo: prevRec.ExportedTo})

	out := []store.RunRecord{coord}
	s.withCompile(coord.ID, func() {
		for i := range first {
			if s.coordinatorStopped(coord.ID) {
				break
			}
			r, err := s.startSection(ctx, coord, plan, i, false, slots)
			if r.ID == "" {
				plan.Sections[i].Error = errText(err)
				s.Logf("mindmap: compile %s section %q: %v", coord.ID, plan.Sections[i].Title, err)
			} else {
				plan.Sections[i].RunID = r.ID
				out = append(out, s.stopIfCoordinatorStopped(ctx, coord.ID, r))
			}
			s.savePlan(coord.ID, plan)
		}
		// Slots of the first wave that did not start must be free before
		// the step below reserves its own.
		slots.release()
		s.compileStep(ctx, coord.ID)
	})
	if c, err := s.Store.GetRun(coord.ID); err == nil {
		out[0] = c
	}
	for i := 1; i < len(out); i++ {
		if r, err := s.Store.GetRun(out[i].ID); err == nil {
			out[i] = r
		}
	}
	return out, nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// truncTitle cuts a title to contract.MaxTitleLen on a rune boundary.
func truncTitle(t string) string {
	if len(t) <= contract.MaxTitleLen {
		return t
	}
	cut := contract.MaxTitleLen - 3
	for cut > 0 && t[cut]&0xC0 == 0x80 {
		cut--
	}
	return t[:cut] + "..."
}

func errText(err error) string {
	if err == nil {
		return "could not start"
	}
	return err.Error()
}

// freeRunSlots is how many more runs the map's concurrency cap allows now,
// counting slots other starts have reserved (MaxCompileParallel when the
// map has no cap). It is a hint: only reserveRunSlots holds a slot.
func (s *Service) freeRunSlots(mapID string) (int, error) {
	m, err := s.Store.GetMap(mapID)
	if err != nil {
		return 0, err
	}
	caps, err := s.runCaps(m)
	if err != nil {
		return 0, err
	}
	if caps.MaxConcurrent <= 0 {
		return MaxCompileParallel, nil
	}
	s.capsMu.Lock()
	defer s.capsMu.Unlock()
	active, err := s.Store.CountActiveRuns(mapID)
	if err != nil {
		return 0, err
	}
	return max(0, caps.MaxConcurrent-active-s.pendingRuns[mapID]), nil
}

// reserveCompileSlot reserves one run slot for a compile's next section or
// synthesis run. full is true when the map's concurrency cap has no slot
// free: the caller waits and steps again once a run ends. Any other
// refusal (a budget cap) is returned as err.
func (s *Service) reserveCompileSlot(mapID string, b *contract.Budget) (slot *runSlots, full bool, err error) {
	slot, err = s.reserveRunSlots(mapID, b, 1)
	if err == nil {
		return slot, false, nil
	}
	if free, ferr := s.freeRunSlots(mapID); ferr == nil && free == 0 {
		return nil, true, nil
	}
	return nil, false, err
}

// kickCompiles steps the running compiles on a map other than skip, so a
// section or synthesis waiting for a run slot starts once an unrelated run
// on the map ends.
func (s *Service) kickCompiles(mapID, skip string) {
	runs, err := s.Store.ListActiveRuns()
	if err != nil {
		return
	}
	for _, r := range runs {
		if r.MapID == mapID && r.ID != skip && r.Template == contract.TemplateCompile && isCoordinator(r) && !r.StopRequested {
			s.syncCompile(r.ID)
		}
	}
}

// previousDocs finds the doc tree an earlier compile of the node wrote
// under the same slug (the newest when there are several), so a recompile
// updates it instead of adding a second tree.
func (s *Service) previousDocs(mapID, nodeID, slug string) (string, docRecord) {
	rootID := ""
	var rec docRecord
	_ = s.Store.View(mapID, func(_ contract.Map, nodes map[string]*contract.Node, _ map[string]*contract.Link) error {
		kids := childIndex(nodes)
		for _, r := range docRoots(nodes) {
			if r.ParentID != nodeID {
				continue
			}
			if sl, _ := s.docSlug(r, kids); sl == slug {
				rootID = r.ID
				rec, _ = s.loadDocRecord(r.ID)
				return nil
			}
		}
		return nil
	})
	return rootID, rec
}

// addDocPlaceholders lays out the doc tree as the human who started the
// compile: the doc root (the overview index) under the node, the single doc
// and one doc per section under it. When plan.DocRootID names the tree of
// an earlier compile, its docs are updated in place (titles, paths and
// sources; bodies stay until an agent rewrites them): the single doc by the
// recorded id or its path, a section doc by its path or else its source
// branch, a sub-doc by its path. Docs with no match are created.
func (s *Service) addDocPlaceholders(plan *compilePlan, node contract.Node, prev docRecord) error {
	last, rootLast := "", ""
	meta := map[string]contract.DocMeta{}
	byPath, bySource, subByPath := map[string]string{}, map[string]string{}, map[string]string{}
	singleID := ""
	_ = s.Store.View(plan.MapID, func(_ contract.Map, nodes map[string]*contract.Node, _ map[string]*contract.Link) error {
		kids := childIndex(nodes)
		for _, n := range kids[node.ID] {
			last = max(last, n.Order)
		}
		if r := nodes[plan.DocRootID]; r == nil || r.Deleted || r.Kind != contract.KindDoc || r.ParentID != node.ID {
			plan.DocRootID = ""
			return nil
		}
		meta[plan.DocRootID] = *nodes[plan.DocRootID].Doc
		for _, c := range kids[plan.DocRootID] {
			rootLast = max(rootLast, c.Order)
			if c.Kind != contract.KindDoc || c.Doc == nil {
				continue
			}
			meta[c.ID] = *c.Doc
			if c.ID == prev.SingleDocID || singleID == "" && c.Doc.Path == plan.Slug+".md" {
				singleID = c.ID
			}
			byPath[c.Doc.Path] = c.ID
			if len(c.Doc.SourceIDs) > 0 && bySource[c.Doc.SourceIDs[0]] == "" {
				bySource[c.Doc.SourceIDs[0]] = c.ID
			}
			for _, g := range kids[c.ID] {
				if g.Kind == contract.KindDoc && g.Doc != nil {
					subByPath[c.ID+"/"+g.Doc.Path] = g.ID
				}
			}
		}
		return nil
	})
	pending := "_Compiling… an agent is writing this doc._"
	var ops []contract.Op
	used := map[string]bool{}
	// put creates a doc, or updates the existing one with id.
	put := func(id, parent string, order *string, title, path string, sources []string) string {
		if old, ok := meta[id]; ok && !used[id] {
			used[id] = true
			old.Path, old.SourceIDs = path, sources
			ops = append(ops, contract.Op{ID: store.NewID(contract.PrefixOp), MapID: plan.MapID, Type: contract.OpNodeUpdate,
				Payload: &contract.NodeUpdatePayload{ID: id, Set: &contract.NodeFields{Title: &title, Doc: &old}}})
			return id
		}
		id = store.NewID(contract.PrefixNode)
		*order = contract.OrderBetween(*order, "")
		ops = append(ops, contract.Op{ID: store.NewID(contract.PrefixOp), MapID: plan.MapID, Type: contract.OpNodeCreate, Payload: &contract.NodeCreatePayload{
			Node: contract.NodeInit{ID: id, ParentID: parent, Order: *order, Title: title, BodyMD: pending, Kind: contract.KindDoc,
				Doc: &contract.DocMeta{Path: path, SourceIDs: sources}}}})
		return id
	}
	plan.DocRootID = put(plan.DocRootID, node.ID, &last, truncTitle(node.Title+" · docs"), "index.md", []string{node.ID})
	plan.SingleDocID = put(singleID, plan.DocRootID, &rootLast, truncTitle(node.Title), plan.Slug+".md", []string{node.ID})
	for i := range plan.Sections {
		sec := &plan.Sections[i]
		id := byPath[sec.Dir+"/index.md"]
		if id == "" || used[id] {
			id = bySource[sec.NodeID]
		}
		sec.DocID = put(id, plan.DocRootID, &rootLast, sec.Title, sec.Dir+"/index.md", []string{sec.NodeID})
		if id != sec.DocID {
			continue
		}
		for j := range sec.Children {
			c := &sec.Children[j]
			if d := subByPath[sec.DocID+"/"+sec.Dir+"/"+c.File]; d != "" {
				c.DocID = d
			}
		}
	}
	_, err := s.Submit(plan.MapID, ops, SubmitOptions{Actor: contract.Actor{ParticipantID: plan.StartedBy}, NoLocks: true})
	return err
}

// startSection starts the section run of plan.Sections[i], taking a slot
// from slots (nil only for a run held for approval). noHold skips the
// guest-approval hold (the host approved the coordinator already).
func (s *Service) startSection(ctx context.Context, coord store.RunRecord, plan compilePlan, i int, noHold bool, slots *runSlots) (store.RunRecord, error) {
	sec := plan.Sections[i]
	b := plan.Section
	return s.StartRun(ctx, RunSpec{NewRun: store.NewRun{MapID: plan.MapID, NodeID: sec.NodeID, ProfileID: plan.ProfileID, ProfileName: plan.ProfileName,
		Harness: plan.Harness, Template: contract.TemplateCompile, Mode: coord.Mode, Budget: &b, ParentRunID: coord.ID,
		Goal: fmt.Sprintf("Write the \"%s\" section of the compiled docs.", sec.Title), StartedBy: plan.StartedBy}, CapsChecked: true, Slots: slots, NoHold: noHold})
}

func (s *Service) startSynthesis(ctx context.Context, coord store.RunRecord, plan compilePlan, slots *runSlots) (store.RunRecord, error) {
	b := CompileSynthesisBudget
	return s.StartRun(ctx, RunSpec{NewRun: store.NewRun{MapID: plan.MapID, NodeID: plan.NodeID, ProfileID: plan.ProfileID, ProfileName: plan.ProfileName,
		Harness: plan.Harness, Template: contract.TemplateSynthesis, Mode: coord.Mode, Budget: &b, ParentRunID: coord.ID,
		Goal: "Write the overview index and the single consolidated doc.", StartedBy: plan.StartedBy}, RepoID: plan.RepoID, CapsChecked: true, Slots: slots, NoHold: true})
}

// syncCompile advances a compile after one of its runs changed.
func (s *Service) syncCompile(coordID string) {
	s.withCompile(coordID, func() { s.compileStep(context.Background(), coordID) })
}

// withCompile runs fn as the only stepper of a compile. Launching a run can
// update it, which calls back into syncCompile on the same goroutine; such
// calls (and calls from other goroutines meanwhile) only mark the compile
// dirty, and the owner steps it again when fn returns.
func (s *Service) withCompile(coordID string, fn func()) {
	s.compileMu.Lock()
	if s.compileBusy == nil {
		s.compileBusy, s.compileDirty = map[string]bool{}, map[string]bool{}
	}
	if s.compileBusy[coordID] {
		s.compileDirty[coordID] = true
		s.compileMu.Unlock()
		return
	}
	s.compileBusy[coordID] = true
	s.compileMu.Unlock()
	fn()
	for {
		s.compileMu.Lock()
		if !s.compileDirty[coordID] {
			delete(s.compileBusy, coordID)
			s.compileMu.Unlock()
			return
		}
		delete(s.compileDirty, coordID)
		s.compileMu.Unlock()
		s.compileStep(context.Background(), coordID)
	}
}

// compileStep starts queued section runs while there is room (at most
// MaxCompileParallel unsettled, within the map's free run slots), starts the
// synthesis once every section has settled, sums up the coordinator's
// status and, for a folder target, writes the docs to disk when the
// synthesis is done. Callers go through withCompile.
func (s *Service) compileStep(ctx context.Context, coordID string) {
	coord, err := s.Store.GetRun(coordID)
	if err != nil || !isCoordinator(coord) || coord.Template != contract.TemplateCompile {
		return
	}
	plan, ok := s.loadPlan(coordID)
	if !ok {
		return
	}
	kids, _ := s.Store.ChildRuns(coordID)
	held := slices.ContainsFunc(kids, func(k store.RunRecord) bool {
		return k.Status == contract.RunWaiting && k.WaitingReason == contract.WaitingApproval
	})
	runOf := func(id string) (store.RunRecord, bool) {
		i := slices.IndexFunc(kids, func(k store.RunRecord) bool { return k.ID == id })
		if i < 0 {
			return store.RunRecord{}, false
		}
		return kids[i], true
	}
	stopping := coord.StopRequested || coord.Status.Terminal()
	if !stopping && !held {
		active := 0
		for _, sec := range plan.Sections {
			if r, ok := runOf(sec.RunID); ok && !settled(r) {
				active++
			}
		}
		for i := range plan.Sections {
			sec := &plan.Sections[i]
			if sec.RunID != "" || sec.Error != "" {
				continue
			}
			if active >= MaxCompileParallel || s.coordinatorStopped(coordID) {
				break
			}
			// Wait for a free run slot: the map's cap counts every active
			// run, so the next section starts once a run on the map ends.
			slot, full, err := s.reserveCompileSlot(plan.MapID, &plan.Section)
			if full {
				break
			}
			var r store.RunRecord
			if err == nil {
				r, err = s.startSection(ctx, coord, plan, i, true, slot)
				slot.release()
			}
			if r.ID == "" {
				sec.Error = errText(err)
			} else {
				sec.RunID = r.ID
				active++
				s.stopIfCoordinatorStopped(ctx, coordID, r)
			}
			s.savePlan(coordID, plan)
		}
		kids, _ = s.Store.ChildRuns(coordID)
		if plan.SynthRunID == "" && plan.SynthErr == "" && !s.coordinatorStopped(coordID) && s.sectionsSettled(plan, runOf) {
			changed := true
			if s.anySectionSucceeded(plan, runOf) {
				synth := CompileSynthesisBudget
				slot, full, err := s.reserveCompileSlot(plan.MapID, &synth)
				changed = !full // full: start it once a run on the map ends
				if changed {
					var r store.RunRecord
					if err == nil {
						r, err = s.startSynthesis(ctx, coord, plan, slot)
						slot.release()
					}
					if r.ID == "" {
						plan.SynthErr = "could not start the synthesis agent: " + errText(err)
					} else {
						plan.SynthRunID = r.ID
						s.stopIfCoordinatorStopped(ctx, coordID, r)
					}
				}
			} else {
				plan.SynthErr = "no section agent finished, so there is nothing to synthesise"
			}
			if changed {
				s.savePlan(coordID, plan)
				kids, _ = s.Store.ChildRuns(coordID)
			}
		}
	}

	st, wr := compileStatus(plan, runOf)
	if coord.StopRequested {
		// Sections that never started keep compileStatus at running.
		st, wr = contract.RunStopped, ""
	}
	if st == contract.RunDone && plan.Target == "dir" && !plan.Exported {
		plan.Exported = true
		s.savePlan(coordID, plan)
		if dir, err := s.ExportToDisk(ctx, plan.MapID, plan.DocRootID); err != nil {
			s.Logf("mindmap: compile %s: write docs: %v", coordID, err)
			s.Activity(contract.ActivityEvent{MapID: plan.MapID, Type: contract.ActivityRunStatus, Actor: contract.Actor{ParticipantID: coord.ParticipantID, RunID: coordID},
				Summary: "could not write the compiled docs to disk: " + err.Error(), NodeIDs: []string{plan.DocRootID}})
		} else {
			s.Activity(contract.ActivityEvent{MapID: plan.MapID, Type: contract.ActivityDocWritten, Actor: contract.Actor{ParticipantID: coord.ParticipantID, RunID: coordID},
				Summary: "compiled docs written to " + dir, NodeIDs: []string{plan.DocRootID}})
		}
	}
	var usage contract.BudgetUsage
	for _, k := range kids {
		usage.Nodes += k.Usage.Nodes
		usage.Depth = max(usage.Depth, k.Usage.Depth)
	}
	errMsg := plan.SynthErr
	statusChanged := false
	r, saved, err := s.updateRun(coordID, func(r *store.RunRecord) bool {
		usage.Subagents = r.Usage.Subagents
		e := r.Error
		if st == contract.RunFailed && e == "" {
			e = errMsg
		}
		if r.Status == st && r.WaitingReason == wr && r.Usage == usage && r.Error == e {
			return false
		}
		statusChanged = r.Status != st || r.WaitingReason != wr
		r.Status, r.WaitingReason, r.Usage, r.Error = st, wr, usage, e
		return true
	})
	if err != nil || !saved || !statusChanged {
		return
	}
	s.SetRunNodeStatus(r)
	s.Activity(contract.ActivityEvent{MapID: r.MapID, Type: contract.ActivityRunStatus, Actor: contract.Actor{ParticipantID: r.ParticipantID, RunID: r.ID},
		Summary: "compile is " + statusText(r), NodeIDs: []string{r.NodeID}})
}

func (s *Service) sectionsSettled(plan compilePlan, runOf func(string) (store.RunRecord, bool)) bool {
	for _, sec := range plan.Sections {
		if sec.Error != "" {
			continue
		}
		r, ok := runOf(sec.RunID)
		if !ok || !settled(r) {
			return false
		}
	}
	return true
}

func (s *Service) anySectionSucceeded(plan compilePlan, runOf func(string) (store.RunRecord, bool)) bool {
	for _, sec := range plan.Sections {
		if r, ok := runOf(sec.RunID); ok && succeeded(r) {
			return true
		}
	}
	return false
}

// compileStatus sums up a compile: the synthesis run once it exists,
// failed when it cannot start, else the section runs (running while any
// section is still to start).
func compileStatus(plan compilePlan, runOf func(string) (store.RunRecord, bool)) (contract.RunStatus, contract.WaitingReason) {
	if r, ok := runOf(plan.SynthRunID); ok {
		if r.Status == contract.RunWaiting && r.WaitingReason == contract.WaitingTurnEnded {
			return contract.RunDone, "" // the agent ended its turn without a report: the docs are what it wrote
		}
		return coordinatorStatus([]store.RunRecord{r})
	}
	if plan.SynthErr != "" {
		return contract.RunFailed, ""
	}
	var sections []store.RunRecord
	for _, sec := range plan.Sections {
		if r, ok := runOf(sec.RunID); ok {
			sections = append(sections, r)
		}
	}
	// Until the synthesis starts the compile is running, unless a section
	// waits on a human (a question, or the host's approval).
	if st, wr := coordinatorStatus(sections); len(sections) > 0 && st == contract.RunWaiting && wr != contract.WaitingTurnEnded {
		return st, wr
	}
	return contract.RunRunning, ""
}

// --- prompts ---------------------------------------------------------------------

// compileBrief is the doc layout part of a section or synthesis prompt.
func (s *Service) compileBrief(r store.RunRecord) string {
	if (r.Template != contract.TemplateCompile && r.Template != contract.TemplateSynthesis) || r.ParentRunID == "" {
		return ""
	}
	plan, ok := s.loadPlan(r.ParentRunID)
	if !ok {
		return ""
	}
	brief := ""
	if r.Template == contract.TemplateSynthesis {
		brief = s.synthesisBrief(plan)
	}
	for _, sec := range plan.Sections {
		if brief == "" && sec.NodeID == r.NodeID {
			brief = sectionBrief(plan, sec)
		}
	}
	if brief != "" && r.Mode == contract.ModePropose {
		brief += "- Propose mode covers your other edits only: `write_doc` into this doc tree is applied to the map at once, so the other compile agents can read it.\n"
	}
	return brief
}

// LiveCompileDoc reports whether a write_doc by run on docID (the doc it
// updates, or the parent of a new doc) goes to the live map even in
// propose mode. A compile's section and synthesis runs write their own
// doc tree live: the synthesis run reads the section docs from the map.
func (s *Service) LiveCompileDoc(run store.RunRecord, docID string) bool {
	if run.Mode != contract.ModePropose || run.Template != contract.TemplateCompile && run.Template != contract.TemplateSynthesis || run.ParentRunID == "" {
		return false
	}
	plan, ok := s.loadPlan(run.ParentRunID)
	if !ok || plan.MapID != run.MapID || plan.DocRootID == "" {
		return false
	}
	live := false
	_ = s.Store.View(plan.MapID, func(_ contract.Map, nodes map[string]*contract.Node, _ map[string]*contract.Link) error {
		n := nodes[docID]
		for depth := 0; n != nil && !n.Deleted && n.Kind == contract.KindDoc && depth < 64; depth++ {
			if n.ID == plan.DocRootID {
				live = true
				break
			}
			n = nodes[n.ParentID]
		}
		return nil
	})
	return live
}

func layoutBlock(slug string) string {
	return "```\n" +
		slug + ".md                    single readable doc (synthesis agent)\n" +
		slug + "/index.md              overview and table of contents (synthesis agent)\n" +
		slug + "/<section>/index.md    one doc per top-level branch (section agents)\n" +
		slug + "/<section>/<sub>.md    sub-docs of a large branch\n" +
		slug + "/assets/               images\n" +
		"```\n"
}

func sectionBrief(plan compilePlan, sec SectionPlan) string {
	var b strings.Builder
	b.WriteString("## Compile brief\n\n")
	fmt.Fprintf(&b, "You write one section of the compiled docs of \"%s\". The docs are laid out like this (`write_doc` paths are relative to the `%s/` folder):\n\n", plan.Title, plan.Slug)
	b.WriteString(layoutBlock(plan.Slug))
	fmt.Fprintf(&b, "\n- Your branch: [%s] \"%s\" (%d direct children, about %d words).\n", sec.NodeID, sec.Title, len(sec.Children), sec.Words)
	fmt.Fprintf(&b, "- Your section doc already exists: [%s]. Replace it with `write_doc` and `doc_id: \"%s\"`, `path: \"%s/index.md\"`.\n", sec.DocID, sec.DocID, sec.Dir)
	if sec.Split {
		fmt.Fprintf(&b, "- This branch is large (%d children, about %d words), so split it. Make `%s/index.md` a short overview of the branch that links to one sub-doc per child branch. Write each sub-doc with `write_doc` and `parent_doc_id: \"%s\"`, or `doc_id` where it already exists:\n",
			len(sec.Children), sec.Words, sec.Dir, sec.DocID)
		for _, c := range sec.Children {
			existing := ""
			if c.DocID != "" {
				existing = fmt.Sprintf(", exists: `doc_id: \"%s\"`", c.DocID)
			}
			fmt.Fprintf(&b, "  - [%s] \"%s\" → path `%s/%s` (link to it from the overview as `%s`%s)\n", c.NodeID, c.Title, sec.Dir, c.File, c.File, existing)
		}
	} else {
		b.WriteString("- Write one doc for the whole branch. Do not create sub-docs.\n")
	}
	var others []SectionPlan
	for _, o := range plan.Sections {
		if o.NodeID != sec.NodeID {
			others = append(others, o)
		}
	}
	if len(others) > 0 {
		b.WriteString("- Other sections, for cross-links (relative to your section folder):\n")
		for _, o := range others {
			fmt.Fprintf(&b, "  - \"%s\" → `../%s/index.md`\n", o.Title, o.Dir)
		}
	}
	b.WriteString("- Pass the ids of the nodes each doc draws on as `source_ids`.\n")
	return b.String()
}

func (s *Service) synthesisBrief(plan compilePlan) string {
	var b strings.Builder
	b.WriteString("## Compile brief\n\n")
	fmt.Fprintf(&b, "You finish the compiled docs of \"%s\" (slug `%s`). The section agents have written their docs. The layout (`write_doc` paths are relative to the `%s/` folder, except the single doc):\n\n",
		plan.Title, plan.Slug, plan.Slug)
	b.WriteString(layoutBlock(plan.Slug))
	fmt.Fprintf(&b, "\n- Read every doc first: `read` with `node_id: \"%s\"`, `depth: 4` and `include_bodies: true`.\n", plan.DocRootID)
	fmt.Fprintf(&b, "- Overview: replace [%s] with `write_doc`, `doc_id: \"%s\"`, `path: \"index.md\"`. Give a short overview of the whole branch and a table of contents that links each section as `<section>/index.md`.\n",
		plan.DocRootID, plan.DocRootID)
	fmt.Fprintf(&b, "- Single doc: replace [%s] with `write_doc`, `doc_id: \"%s\"`, `path: \"%s.md\"`. Edit every section, in order, into one document a person can read from start to finish. Keep the citations. It sits next to the `%s/` folder, so links into the folder start with `%s/`.\n",
		plan.SingleDocID, plan.SingleDocID, plan.Slug, plan.Slug, plan.Slug)
	b.WriteString("- Sections:\n")
	for _, sec := range plan.Sections {
		state := ""
		if sec.Error != "" {
			state = " (its agent did not start: write this section yourself from the branch)"
		} else if r, err := s.Store.GetRun(sec.RunID); err == nil && !succeeded(r) {
			state = fmt.Sprintf(" (its agent %s: if the doc is still a placeholder or out of date, write it yourself from the branch)", r.Status)
		}
		fmt.Fprintf(&b, "  - [%s] \"%s\" → `%s/index.md`, from branch [%s]%s\n", sec.DocID, sec.Title, sec.Dir, sec.NodeID, state)
	}
	if plan.Target == "repo" {
		fmt.Fprintf(&b, "- This task has the map's repository checked out on its own branch. Once the doc nodes are written, write the same files under `docs/mindmaps/`: `docs/mindmaps/%s.md`, `docs/mindmaps/%s/index.md` and every section and sub-doc at its path under `docs/mindmaps/%s/`, plus `docs/mindmaps/%s/assets/.gitkeep`. Commit them on your branch with the message `docs: compile mind map %s`. Do not push to the default branch.\n",
			plan.Slug, plan.Slug, plan.Slug, plan.Slug, plan.Slug)
	} else {
		b.WriteString("- When you finish, the plugin writes the doc nodes to the export folder on disk. Do not write files yourself.\n")
	}
	return b.String()
}
