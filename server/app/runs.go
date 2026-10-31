package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/store"
)

// RunSpec is a run to start: the store fields plus launch-only options.
type RunSpec struct {
	store.NewRun
	RepoID string // Kandev repository for the task (not stored)
	// ClaimNode claims the run's node for its agent before launch (swarm
	// lens branches), so other agents leave it alone from the start.
	ClaimNode bool
	// CapsChecked skips CheckRunCaps when the caller already checked it for
	// a batch (a swarm checks once for all its lens runs).
	CapsChecked bool
}

// StartRun creates a run (row, agent participant, run token, proposal,
// budget charge for children), launches its Kandev task with a prompt for
// the run's harness, records the task id and broadcasts. A launch failure
// leaves the run failed and returns it together with an unavailable error.
// A guest's run on a map that requires approval is created but held in
// waiting(approval) until the host calls ApproveRun.
func (s *Service) StartRun(ctx context.Context, spec RunSpec) (store.RunRecord, error) {
	nr, err := s.clampGuestBudget(spec.NewRun)
	if err != nil {
		return store.RunRecord{}, err
	}
	spec.NewRun = nr
	if !spec.CapsChecked {
		if err := s.CheckRunCaps(spec.MapID, spec.Budget, 1); err != nil {
			return store.RunRecord{}, err
		}
	}
	hold, err := s.needsApproval(spec.NewRun)
	if err != nil {
		return store.RunRecord{}, err
	}
	if err := s.resolveProfile(ctx, &spec.NewRun); err != nil {
		return store.RunRecord{}, err
	}
	if spec.Template == contract.TemplateGarden {
		spec.Mode = contract.ModePropose // the gardener always proposes
	}
	if spec.Transport == "" {
		spec.Transport = TransportFor(spec.Harness)
	}
	run, token, err := s.Store.CreateRun(spec.NewRun, s.Port())
	if err != nil {
		return run, err
	}
	node, _ := s.Store.Node(run.MapID, run.NodeID)
	if hold {
		_ = s.Store.RevokeRunTokens(run.ID)
		s.rememberRepo(run.ID, spec.RepoID)
		run, _ = s.UpdateRun(run.ID, func(r *store.RunRecord) bool {
			r.Status, r.WaitingReason = contract.RunWaiting, contract.WaitingApproval
			return true
		})
		s.announceRun(run, node)
		return run, nil
	}
	s.announceRun(run, node)
	if spec.ClaimNode {
		s.claimFor(run, run.NodeID)
	}
	return s.launch(ctx, run, node, token, spec.RepoID)
}

// needsApproval reports whether a top-level run started by a guest must wait
// for the host. Hosts, agents and spawned runs are never held.
func (s *Service) needsApproval(nr store.NewRun) (bool, error) {
	if nr.StartedBy == "" || nr.ParentRunID != "" && !s.parentIsCoordinator(nr.ParentRunID) {
		return false, nil
	}
	return s.GuestRunNeedsApproval(nr.MapID, nr.StartedBy)
}

func (s *Service) parentIsCoordinator(runID string) bool {
	r, err := s.Store.GetRun(runID)
	return err == nil && isCoordinator(r)
}

// launch creates the run's Kandev task and records it.
func (s *Service) launch(ctx context.Context, run store.RunRecord, node contract.Node, token, repoID string) (store.RunRecord, error) {
	res, err := s.Launcher.Launch(ctx, LaunchRequest{
		Run:         run,
		Title:       taskTitle(run, node),
		Description: taskDescription(run, node, s.mapTitle(run.MapID)),
		Prompt:      BuildPrompt(s.promptInput(run, node, token)),
		RunToken:    token,
		RepoID:      repoID,
	})
	if err != nil {
		_ = s.Store.RevokeRunTokens(run.ID)
		run, _ = s.UpdateRun(run.ID, func(r *store.RunRecord) bool {
			r.Status, r.WaitingReason, r.Error = contract.RunFailed, "", "launch failed: "+err.Error()
			return true
		})
		s.SetRunNodeStatus(run)
		s.releaseClaims(run)
		return run, contract.Errf(contract.ErrUnavailable, "could not launch the agent task: %v", err)
	}
	if res.TaskID != "" {
		if err := s.Store.SetRunTask(run.ID, res.TaskID, res.SessionID); err != nil {
			return run, err
		}
		run.TaskID, run.SessionID = res.TaskID, res.SessionID
		s.PublishRun(run)
		if s.OnTaskBound != nil {
			s.OnTaskBound(res.TaskID)
		}
	}
	return run, nil
}

// rememberRepo keeps a held run's repository until it is approved. It is
// not stored: after a plugin restart an approved execute run falls back to
// the map's repository.
func (s *Service) rememberRepo(runID, repoID string) {
	if repoID == "" {
		return
	}
	s.heldMu.Lock()
	defer s.heldMu.Unlock()
	if s.heldRepos == nil {
		s.heldRepos = map[string]string{}
	}
	s.heldRepos[runID] = repoID
}

func (s *Service) takeRepo(runID string) string {
	s.heldMu.Lock()
	defer s.heldMu.Unlock()
	repo := s.heldRepos[runID]
	delete(s.heldRepos, runID)
	return repo
}

// ApproveRun launches a run held in waiting(approval). A swarm coordinator
// approves all its held lens runs. Approving anything else is a conflict.
func (s *Service) ApproveRun(ctx context.Context, runID string) ([]store.RunRecord, error) {
	run, err := s.Store.GetRun(runID)
	if err != nil {
		return nil, err
	}
	if isCoordinator(run) {
		kids, err := s.Store.ChildRuns(run.ID)
		if err != nil {
			return nil, err
		}
		var out []store.RunRecord
		for _, k := range kids {
			if k.Status == contract.RunWaiting && k.WaitingReason == contract.WaitingApproval {
				r, err := s.ApproveRun(ctx, k.ID)
				if err != nil && len(r) == 0 {
					return out, err
				}
				out = append(out, r...)
			}
		}
		if len(out) == 0 {
			return nil, contract.Errf(contract.ErrConflict, "no run in this swarm is waiting for approval")
		}
		return out, nil
	}
	if run.Status != contract.RunWaiting || run.WaitingReason != contract.WaitingApproval || run.TaskID != "" {
		return nil, contract.Errf(contract.ErrConflict, "this run is not waiting for approval")
	}
	if err := s.CheckRunCaps(run.MapID, &run.Budget, 1); err != nil {
		return nil, err
	}
	token, err := s.Store.IssueRunToken(run, s.Port())
	if err != nil {
		return nil, err
	}
	run, err = s.UpdateRun(run.ID, func(r *store.RunRecord) bool {
		if r.Status != contract.RunWaiting || r.WaitingReason != contract.WaitingApproval {
			return false
		}
		r.Status, r.WaitingReason = contract.RunQueued, ""
		return true
	})
	if err != nil {
		return nil, err
	}
	if run.Status != contract.RunQueued {
		return nil, contract.Errf(contract.ErrConflict, "this run is not waiting for approval")
	}
	node, _ := s.Store.Node(run.MapID, run.NodeID)
	s.SetRunNodeStatus(run)
	s.Activity(contract.ActivityEvent{MapID: run.MapID, Type: contract.ActivityRunStatus, Actor: contract.Actor{ParticipantID: run.StartedBy},
		Summary: fmt.Sprintf("the host approved the %s agent", label(run)), NodeIDs: []string{run.NodeID}})
	if run.Lens != "" && run.SwarmID != "" {
		s.claimFor(run, run.NodeID)
	}
	r, err := s.launch(ctx, run, node, token, s.takeRepo(run.ID))
	return []store.RunRecord{r}, err
}

// announceRun broadcasts a new run, its participant and proposal, and the
// activity line, and moves the node's ring.
func (s *Service) announceRun(run store.RunRecord, node contract.Node) {
	if p, err := s.Store.GetParticipant(run.ParticipantID); err == nil {
		s.Hub.Publish(run.MapID, contract.Message{Type: contract.MsgParticipant, Payload: &contract.ParticipantPayload{Participant: p}})
	}
	s.PublishRun(run)
	if run.ProposalID != "" {
		if p, err := s.Store.GetProposal(run.ProposalID); err == nil {
			s.Hub.Publish(run.MapID, contract.Message{Type: contract.MsgProposal, Payload: &contract.ProposalPayload{Proposal: p}})
		}
	}
	typ, verb := contract.ActivityRunStarted, "started"
	actor := contract.Actor{ParticipantID: run.StartedBy}
	if run.ParentRunID != "" {
		typ, verb = contract.ActivityRunSpawned, "spawned"
		if parent, err := s.Store.GetRun(run.ParentRunID); err == nil && !isCoordinator(parent) {
			actor = contract.Actor{ParticipantID: parent.ParticipantID, RunID: parent.ID}
		}
	}
	s.Activity(contract.ActivityEvent{MapID: run.MapID, Type: typ, Actor: actor, NodeIDs: []string{run.NodeID},
		Summary: fmt.Sprintf("%s %s on \"%s\"", s.actorName(actor.ParticipantID), verb+" "+article(label(run))+" "+label(run)+agentSuffix(run), node.Title)})
	s.SetRunNodeStatus(run)
}

func agentSuffix(r store.RunRecord) string {
	if r.ProfileName != "" {
		return " agent (" + r.ProfileName + ")"
	}
	return " agent"
}

func article(word string) string {
	if word != "" && strings.ContainsRune("aeiouAEIOU", rune(word[0])) {
		return "an"
	}
	return "a"
}

func (s *Service) actorName(participantID string) string {
	if p, err := s.Store.GetParticipant(participantID); err == nil && p.Name != "" {
		return p.Name
	}
	return "Someone"
}

func (s *Service) mapTitle(mapID string) string {
	if m, err := s.Store.GetMap(mapID); err == nil {
		return m.Title
	}
	return ""
}

func taskTitle(r store.RunRecord, n contract.Node) string {
	t := "Mind map: " + label(r) + " · " + n.Title
	if len(t) > 180 {
		t = t[:177] + "..."
	}
	return t
}

func taskDescription(r store.RunRecord, n contract.Node, mapTitle string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "A %s agent started from the node \"%s\" in the mind map \"%s\" (Kandev Mind Map plugin).\n\n", label(r), n.Title, mapTitle)
	if r.Goal != "" {
		b.WriteString("Goal: " + r.Goal + "\n\n")
	}
	b.WriteString("Open the Mind map panel in this task to see the node's branch and jump back to the map.")
	return b.String()
}

func (s *Service) promptInput(r store.RunRecord, n contract.Node, token string) PromptInput {
	in := PromptInput{Run: r, Node: n, Token: token}
	_ = s.Store.View(r.MapID, func(m contract.Map, nodes map[string]*contract.Node, _ map[string]*contract.Link) error {
		in.MapTitle = m.Title
		for cur, i := nodes[n.ID], 0; cur != nil && i < 200; cur, i = nodes[cur.ParentID], i+1 {
			in.Path = append([]string{cur.Title}, in.Path...)
		}
		return nil
	})
	if r.Transport != contract.TransportMCP && s.Outline != nil {
		in.Outline = s.Outline(r)
	}
	return in
}

func label(r store.RunRecord) string {
	if r.Lens != "" {
		return strings.ReplaceAll(string(r.Lens), "_", " ")
	}
	return string(r.Template)
}

// resolveProfile fills the profile id, name and harness from the Kandev
// profile list (by id, else by name). Without a lister (tests) it keeps
// what it was given.
func (s *Service) resolveProfile(ctx context.Context, nr *store.NewRun) error {
	if nr.ProfileID == "" && s.DefaultProfile != nil {
		nr.ProfileID = s.DefaultProfile(ctx)
	}
	if s.Profiles == nil {
		return nil
	}
	if nr.ProfileID == "" {
		return contract.FieldErr(contract.ErrBadRequest, "profile_id", "choose an agent profile: none was given and no default is set")
	}
	list, err := s.Profiles(ctx)
	if err != nil {
		return contract.Errf(contract.ErrUnavailable, "could not list agent profiles: %v", err)
	}
	p, ok := FindProfile(list, nr.ProfileID)
	if !ok {
		return contract.FieldErr(contract.ErrBadRequest, "profile_id", "unknown agent profile %q", nr.ProfileID)
	}
	nr.ProfileID, nr.ProfileName, nr.Harness = p.ID, p.Name, p.Harness
	return nil
}

// FindProfile finds a profile by id, then by case-insensitive name.
func FindProfile(list []contract.AgentProfile, idOrName string) (contract.AgentProfile, bool) {
	for _, p := range list {
		if p.ID == idOrName {
			return p, true
		}
	}
	for _, p := range list {
		if strings.EqualFold(p.Name, strings.TrimSpace(idOrName)) {
			return p, true
		}
	}
	return contract.AgentProfile{}, false
}

// claimFor claims a node for a run's agent until its time budget ends.
func (s *Service) claimFor(r store.RunRecord, nodeID string) {
	ttl := time.Duration(r.Budget.MaxMinutes) * time.Minute
	if ttl <= 0 || ttl > 2*time.Hour {
		ttl = time.Hour
	}
	note := label(r) + " agent"
	op := contract.Op{ID: store.NewID(contract.PrefixOp), MapID: r.MapID, Type: contract.OpNodeClaim, Payload: &contract.NodeClaimPayload{
		ID: nodeID, Claim: contract.Claim{ParticipantID: r.ParticipantID, RunID: r.ID, Note: note, ExpiresAt: time.Now().UTC().Add(ttl)}}}
	if _, err := s.Submit(r.MapID, []contract.Op{op}, SubmitOptions{Actor: contract.Actor{ParticipantID: r.ParticipantID, RunID: r.ID}, NoLocks: true}); err != nil {
		s.Logf("mindmap: claim for run %s: %v", r.ID, err)
	}
}

// releaseClaims releases every claim a run's agent still holds.
func (s *Service) releaseClaims(r store.RunRecord) {
	var ops []contract.Op
	_ = s.Store.View(r.MapID, func(_ contract.Map, nodes map[string]*contract.Node, _ map[string]*contract.Link) error {
		for _, n := range nodes {
			if !n.Deleted && n.Claim != nil && n.Claim.ParticipantID == r.ParticipantID {
				ops = append(ops, contract.Op{ID: store.NewID(contract.PrefixOp), MapID: r.MapID, Type: contract.OpNodeRelease,
					Payload: &contract.NodeReleasePayload{ID: n.ID}})
			}
		}
		return nil
	})
	if len(ops) == 0 {
		return
	}
	if _, err := s.Submit(r.MapID, ops, SubmitOptions{Actor: contract.Actor{ParticipantID: r.ParticipantID, RunID: r.ID}, NoLocks: true}); err != nil {
		s.Logf("mindmap: release claims of run %s: %v", r.ID, err)
	}
}

// --- swarms -------------------------------------------------------------------------

// MaxSwarmAgents caps the lens agents of one swarm.
const MaxSwarmAgents = 8

// SwarmLensBudget is the per-lens default when a swarm request leaves a
// field at zero.
var SwarmLensBudget = contract.Budget{MaxDepth: 3, MaxNodes: 20, MaxSubagents: 0, MaxMinutes: 20}

// SwarmSpec starts one lens agent per member under a coordinator run.
type SwarmSpec struct {
	MapID, NodeID string
	Goal          string
	Mode          contract.RunMode
	Budget        *contract.Budget // per lens agent
	Agents        []contract.SwarmMember
	ProfileID     string // default for members without one
	RepoID        string
	StartedBy     string
}

func isCoordinator(r store.RunRecord) bool {
	return r.Template == contract.TemplateSwarm && r.Lens == "" && r.TaskID == ""
}

// StartSwarm creates a coordinator run on the node (no task: it owns the
// shared budget pool and its status sums up the lens agents), one lens
// branch node per member under the node, and one lens run per branch,
// claimed for its agent. It returns the coordinator first, then the lens
// runs. Lens runs whose launch fails are returned as failed.
func (s *Service) StartSwarm(ctx context.Context, spec SwarmSpec) ([]store.RunRecord, error) {
	if len(spec.Agents) == 0 {
		return nil, contract.FieldErr(contract.ErrBadRequest, "agents", "required: at least one lens agent")
	}
	if len(spec.Agents) > MaxSwarmAgents {
		return nil, contract.FieldErr(contract.ErrTooLarge, "agents", "at most %d lens agents per swarm", MaxSwarmAgents)
	}
	for i, a := range spec.Agents {
		if !a.Lens.Valid() {
			return nil, contract.FieldErr(contract.ErrBadRequest, fmt.Sprintf("agents[%d].lens", i), "unknown lens %q", a.Lens)
		}
	}
	if spec.Mode != "" && !spec.Mode.Valid() {
		return nil, contract.FieldErr(contract.ErrBadRequest, "mode", "must be apply or propose")
	}
	if err := spec.Budget.Validate("budget"); err != nil {
		return nil, err
	}
	// Resolve every profile first, so a bad one fails before anything is created.
	members := make([]store.NewRun, len(spec.Agents))
	for i, a := range spec.Agents {
		nr := store.NewRun{ProfileID: a.ProfileID}
		if nr.ProfileID == "" {
			nr.ProfileID = spec.ProfileID
		}
		if err := s.resolveProfile(ctx, &nr); err != nil {
			e := *contract.AsError(err)
			e.Field = fmt.Sprintf("agents[%d].profile_id", i)
			return nil, &e
		}
		members[i] = nr
	}
	per := SwarmLensBudget
	if b := spec.Budget; b != nil {
		if b.MaxDepth > 0 {
			per.MaxDepth = b.MaxDepth
		}
		if b.MaxNodes > 0 {
			per.MaxNodes = b.MaxNodes
		}
		if b.MaxSubagents > 0 {
			per.MaxSubagents = b.MaxSubagents
		}
		if b.MaxMinutes > 0 {
			per.MaxMinutes = b.MaxMinutes
		}
	}
	n := len(spec.Agents)
	// Lens runs have the coordinator as parent, so StartRun neither clamps
	// nor checks them: do both once here for the whole swarm.
	clamped, err := s.clampGuestBudget(store.NewRun{MapID: spec.MapID, StartedBy: spec.StartedBy, Budget: &per})
	if err != nil {
		return nil, err
	}
	per = *clamped.Budget
	if err := s.CheckRunCaps(spec.MapID, &per, n); err != nil {
		return nil, err
	}
	pool := contract.Budget{MaxDepth: per.MaxDepth + 1, MaxNodes: per.MaxNodes * n, MaxSubagents: n * (1 + per.MaxSubagents), MaxMinutes: per.MaxMinutes}
	swarmID := store.NewID("sw")
	coord, _, err := s.Store.CreateRun(store.NewRun{MapID: spec.MapID, NodeID: spec.NodeID, Template: contract.TemplateSwarm, Mode: spec.Mode,
		Goal: spec.Goal, Budget: &pool, SwarmID: swarmID, Harness: contract.HarnessOther, ProfileName: fmt.Sprintf("%d lenses", n),
		Transport: contract.TransportMCP, StartedBy: spec.StartedBy}, s.Port())
	if err != nil {
		return nil, err
	}
	_ = s.Store.RevokeRunTokens(coord.ID) // no agent behind the coordinator
	node, _ := s.Store.Node(coord.MapID, coord.NodeID)
	coord, _ = s.UpdateRun(coord.ID, func(r *store.RunRecord) bool {
		r.Status = contract.RunRunning
		return true
	})
	s.announceRun(coord, node)

	branches, err := s.addLensBranches(spec, node)
	if err != nil {
		coord, _ = s.UpdateRun(coord.ID, func(r *store.RunRecord) bool {
			r.Status, r.Error = contract.RunFailed, "could not create lens branches: "+err.Error()
			return true
		})
		s.SetRunNodeStatus(coord)
		return []store.RunRecord{coord}, err
	}
	out := []store.RunRecord{coord}
	for i, a := range spec.Agents {
		m := members[i]
		b := per
		r, err := s.StartRun(ctx, RunSpec{NewRun: store.NewRun{MapID: spec.MapID, NodeID: branches[i], ProfileID: m.ProfileID, ProfileName: m.ProfileName,
			Harness: m.Harness, Template: contract.TemplateSwarm, Lens: a.Lens, Mode: spec.Mode, Goal: spec.Goal, Budget: &b,
			ParentRunID: coord.ID, SwarmID: swarmID, StartedBy: spec.StartedBy}, RepoID: spec.RepoID, ClaimNode: true, CapsChecked: true})
		if r.ID == "" && err != nil && s.Logf != nil {
			s.Logf("mindmap: swarm %s lens %s: %v", swarmID, a.Lens, err)
			continue
		}
		out = append(out, r)
	}
	s.syncCoordinator(coord.ID)
	if c, err := s.Store.GetRun(coord.ID); err == nil {
		out[0] = c
	}
	return out, nil
}

// addLensBranches creates one "<Lens> lens" child per member, as the human
// who started the swarm, and returns their ids in member order.
func (s *Service) addLensBranches(spec SwarmSpec, parent contract.Node) ([]string, error) {
	last := ""
	_ = s.Store.View(spec.MapID, func(_ contract.Map, nodes map[string]*contract.Node, _ map[string]*contract.Link) error {
		for _, n := range nodes {
			if !n.Deleted && n.ParentID == parent.ID && n.Order > last {
				last = n.Order
			}
		}
		return nil
	})
	ids := make([]string, len(spec.Agents))
	ops := make([]contract.Op, len(spec.Agents))
	for i, a := range spec.Agents {
		ids[i] = store.NewID(contract.PrefixNode)
		order := contract.OrderBetween(last, "")
		last = order
		lens := LensTitle(a.Lens)
		body := fmt.Sprintf("Branch of the %s agent in a swarm on \"%s\".", lens, parent.Title)
		if spec.Goal != "" {
			body += "\n\nGoal: " + spec.Goal
		}
		ops[i] = contract.Op{ID: store.NewID(contract.PrefixOp), MapID: spec.MapID, Type: contract.OpNodeCreate, Payload: &contract.NodeCreatePayload{
			Node: contract.NodeInit{ID: ids[i], ParentID: parent.ID, Order: order, Title: lens + " lens", BodyMD: body, Kind: contract.KindIdea}}}
	}
	_, err := s.Submit(spec.MapID, ops, SubmitOptions{Actor: contract.Actor{ParticipantID: spec.StartedBy}, NoLocks: true})
	return ids, err
}

// LensTitle is the display name of a lens ("Examples-hunter").
func LensTitle(l contract.Lens) string {
	s := strings.ReplaceAll(string(l), "_", "-")
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// syncCoordinator recomputes a swarm coordinator's status from its lens runs.
func (s *Service) syncCoordinator(runID string) {
	coord, err := s.Store.GetRun(runID)
	if err != nil || !isCoordinator(coord) {
		return
	}
	kids, err := s.Store.ChildRuns(coord.ID)
	if err != nil || len(kids) == 0 {
		return
	}
	st, wr := coordinatorStatus(kids)
	if coord.StopRequested && st.Terminal() {
		st, wr = contract.RunStopped, ""
	}
	var usage contract.BudgetUsage
	for _, k := range kids {
		usage.Nodes += k.Usage.Nodes
		usage.Depth = max(usage.Depth, k.Usage.Depth)
	}
	r, saved, err := s.updateRun(coord.ID, func(r *store.RunRecord) bool {
		if r.Status == st && r.WaitingReason == wr {
			return false
		}
		r.Status, r.WaitingReason = st, wr
		return true
	})
	if err != nil || !saved {
		return
	}
	s.SetRunNodeStatus(r)
	s.Activity(contract.ActivityEvent{MapID: r.MapID, Type: contract.ActivityRunStatus, Actor: contract.Actor{ParticipantID: r.ParticipantID, RunID: r.ID},
		Summary: "swarm is " + statusText(r), NodeIDs: []string{r.NodeID}})
}

// coordinatorStatus sums up lens runs: any active agent keeps the swarm
// running; then anything that needs a human; then review; all stopped or
// all failed carry over; anything else is done.
func coordinatorStatus(kids []store.RunRecord) (contract.RunStatus, contract.WaitingReason) {
	has := func(st contract.RunStatus) bool {
		return slices.ContainsFunc(kids, func(k store.RunRecord) bool { return k.Status == st })
	}
	all := func(st contract.RunStatus) bool {
		return !slices.ContainsFunc(kids, func(k store.RunRecord) bool { return k.Status != st })
	}
	switch {
	case has(contract.RunQueued) || has(contract.RunRunning):
		return contract.RunRunning, ""
	case has(contract.RunWaiting):
		if slices.ContainsFunc(kids, func(k store.RunRecord) bool { return k.WaitingReason == contract.WaitingQuestion }) {
			return contract.RunWaiting, contract.WaitingQuestion
		}
		if slices.ContainsFunc(kids, func(k store.RunRecord) bool { return k.WaitingReason == contract.WaitingApproval }) {
			return contract.RunWaiting, contract.WaitingApproval
		}
		return contract.RunWaiting, contract.WaitingTurnEnded
	case has(contract.RunReview):
		return contract.RunReview, ""
	case all(contract.RunStopped):
		return contract.RunStopped, ""
	case all(contract.RunFailed):
		return contract.RunFailed, ""
	}
	return contract.RunDone, ""
}

func statusText(r store.RunRecord) string {
	if r.WaitingReason != "" {
		return string(r.Status) + " (" + strings.ReplaceAll(string(r.WaitingReason), "_", " ") + ")"
	}
	return string(r.Status)
}

// --- stop, message, answer ------------------------------------------------------------

// StopMessage is what a stopped run's agent is told. There is no Host API to
// stop a session (spike finding 5), so this asks the agent to stop.
const StopMessage = "Stop now: a human stopped this mind map run. Do not call any more mind map tools except `report` " +
	"(status `review`, summarising what you have), then end your turn."

// StopRun stops a run: it is marked stopped at once, its run tokens are
// revoked, its claims released, and its agent is asked to stop through
// Messages().Send. A coordinator stops each of its lens runs. Stopping a
// terminal run is a no-op.
func (s *Service) StopRun(ctx context.Context, runID, by, reason string) (store.RunRecord, error) {
	run, err := s.Store.GetRun(runID)
	if err != nil {
		return run, err
	}
	if run.Status.Terminal() {
		return run, nil
	}
	if isCoordinator(run) {
		kids, _ := s.Store.ChildRuns(run.ID)
		for _, k := range kids {
			if _, err := s.StopRun(ctx, k.ID, by, reason); err != nil {
				s.Logf("mindmap: stop lens run %s: %v", k.ID, err)
			}
		}
	}
	run, err = s.UpdateRun(run.ID, func(r *store.RunRecord) bool {
		r.StopRequested, r.Status, r.WaitingReason = true, contract.RunStopped, ""
		if reason != "" {
			r.Error = reason
		}
		return true
	})
	if err != nil {
		return run, err
	}
	_ = s.Store.RevokeRunTokens(run.ID)
	s.SetRunNodeStatus(run)
	s.releaseClaims(run)
	who := "The host"
	if by != "" {
		who = s.actorName(by)
	}
	summary := fmt.Sprintf("%s stopped the %s agent", who, label(run))
	if reason != "" {
		summary += " (" + reason + ")"
	}
	s.Activity(contract.ActivityEvent{MapID: run.MapID, Type: contract.ActivityRunStatus, Actor: contract.Actor{ParticipantID: by}, Summary: summary, NodeIDs: []string{run.NodeID}})
	if run.TaskID != "" {
		msg := StopMessage
		if reason != "" {
			msg += " Reason: " + reason + "."
		}
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if err := s.Messenger.Send(sctx, run.TaskID, run.SessionID, msg); err != nil {
			s.Logf("mindmap: stop message to run %s: %v", run.ID, err)
		}
	}
	return run, nil
}

// MessageRun sends a human's message to a run's agent. A coordinator
// forwards it to every lens run that has a task. Messaging a stopped run
// re-opens it: the stop flag is cleared and a bridge run gets a fresh token
// (its old one was revoked), so the agent can write again.
func (s *Service) MessageRun(ctx context.Context, runID, by, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return contract.FieldErr(contract.ErrBadRequest, "text", "required")
	}
	if len(text) > contract.MaxBodyLen {
		return contract.FieldErr(contract.ErrTooLarge, "text", "at most %d characters", contract.MaxBodyLen)
	}
	run, err := s.Store.GetRun(runID)
	if err != nil {
		return err
	}
	if isCoordinator(run) {
		kids, _ := s.Store.ChildRuns(run.ID)
		sent := 0
		for _, k := range kids {
			if k.TaskID == "" {
				continue
			}
			if err := s.MessageRun(ctx, k.ID, by, text); err != nil {
				return err
			}
			sent++
		}
		if sent == 0 {
			return contract.Errf(contract.ErrConflict, "no agent in this swarm has a task to message")
		}
		return nil
	}
	if run.TaskID == "" {
		return contract.Errf(contract.ErrConflict, "this run has no Kandev task to message")
	}
	msg := fmt.Sprintf("Message from %s in the mind map:\n\n%s", s.actorName(by), text)
	if run.StopRequested {
		run, err = s.UpdateRun(run.ID, func(r *store.RunRecord) bool {
			r.StopRequested = false
			if r.Error != "" && r.Status == contract.RunStopped {
				r.Error = ""
			}
			return true
		})
		if err != nil {
			return err
		}
		if run.Transport != contract.TransportMCP {
			if tok, err := s.Store.IssueRunToken(run, s.Port()); err == nil {
				msg += "\n\nYour previous run token was revoked. Use this one from now on:\n```\n" + tok + "\n```"
			}
		}
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := s.Messenger.Send(sctx, run.TaskID, run.SessionID, msg); err != nil {
		return contract.Errf(contract.ErrUnavailable, "could not message the agent: %v", err)
	}
	return nil
}

// AnswerQuestion commits a question.answer op as the participant; the
// commit hook relays the answer to the asking agent.
func (s *Service) AnswerQuestion(mapID, nodeID, answer, by string) (store.SubmitResult, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return store.SubmitResult{}, contract.FieldErr(contract.ErrBadRequest, "answer", "required")
	}
	n, err := s.Store.Node(mapID, nodeID)
	if err != nil {
		return store.SubmitResult{}, err
	}
	if n.Question == nil {
		return store.SubmitResult{}, contract.FieldErr(contract.ErrBadRequest, "node_id", "node %s is not an agent question", nodeID)
	}
	op := contract.Op{ID: store.NewID(contract.PrefixOp), MapID: mapID, Type: contract.OpQuestionAnswer,
		Payload: &contract.QuestionAnswerPayload{NodeID: nodeID, Answer: answer}}
	return s.Submit(mapID, []contract.Op{op}, SubmitOptions{Actor: contract.Actor{ParticipantID: by}})
}

// --- time budgets ---------------------------------------------------------------------

// EnforceTimeBudgets stops runs that are past their max_minutes. It is
// called periodically by the plugin.
func (s *Service) EnforceTimeBudgets(ctx context.Context, now time.Time) {
	runs, err := s.Store.ActiveRuns()
	if err != nil {
		return
	}
	for _, r := range runs {
		if r.Budget.MaxMinutes <= 0 || r.CreatedAt.IsZero() || r.Status == contract.RunWaiting && r.WaitingReason == contract.WaitingApproval {
			continue
		}
		if now.Sub(r.CreatedAt) > time.Duration(r.Budget.MaxMinutes)*time.Minute {
			if _, err := s.StopRun(ctx, r.ID, "", fmt.Sprintf("time budget of %d minutes used up", r.Budget.MaxMinutes)); err != nil {
				s.Logf("mindmap: time budget stop %s: %v", r.ID, err)
			}
		}
	}
}

// --- task panel -----------------------------------------------------------------------

// MaxTaskBranch caps TaskRunInfo.Branch.
const MaxTaskBranch = 200

// TaskRunInfo returns the run behind a Kandev task with its node's path and
// branch, for the task panel.
func (s *Service) TaskRunInfo(taskID string) (contract.TaskRunInfo, error) {
	run, err := s.Store.RunByTask(taskID)
	if err != nil {
		return contract.TaskRunInfo{}, contract.Errf(contract.ErrNotFound, "task %s is not a mind map run", taskID)
	}
	out := contract.TaskRunInfo{Run: run.Run, Path: []contract.NodeRef{}, Branch: []contract.NodeRef{}}
	err = s.Store.View(run.MapID, func(m contract.Map, nodes map[string]*contract.Node, _ map[string]*contract.Link) error {
		out.MapTitle = m.Title
		var path []*contract.Node
		for cur, i := nodes[run.NodeID], 0; cur != nil && i < 200; cur, i = nodes[cur.ParentID], i+1 {
			path = append([]*contract.Node{cur}, path...)
		}
		for d, n := range path {
			out.Path = append(out.Path, nodeRef(n, d))
		}
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
		var walk func(n *contract.Node, d int)
		walk = func(n *contract.Node, d int) {
			if len(out.Branch) >= MaxTaskBranch {
				out.More = true
				return
			}
			out.Branch = append(out.Branch, nodeRef(n, d))
			for _, c := range kids[n.ID] {
				walk(c, d+1)
			}
		}
		if n := nodes[run.NodeID]; n != nil && !n.Deleted {
			walk(n, 0)
		}
		return nil
	})
	return out, err
}

func nodeRef(n *contract.Node, depth int) contract.NodeRef {
	return contract.NodeRef{ID: n.ID, Title: n.Title, Kind: n.Kind, Status: n.Status, Depth: depth}
}
