// Package app is the plugin core: the store plus the hub plus the Kandev
// side effects (launching tasks, messaging agents). Every write path (WS
// submit, long-poll submit, agent tools, proposal review, events) goes
// through Service so that committing and broadcasting stay in one order.
package app

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/hub"
	"kandev-plugin-mindmap/server/store"
)

// LaunchRequest is what a Launcher needs to start a run's Kandev task.
type LaunchRequest struct {
	Run         store.RunRecord
	Title       string
	Description string
	Prompt      string
	RunToken    string
	RepoID      string // optional Kandev repository for the task
}

// LaunchResult identifies the Kandev task a launch created.
type LaunchResult struct {
	TaskID    string
	SessionID string
}

// Launcher starts the Kandev task behind a run (Host.Tasks().Create in the
// plugin; NoopLauncher and FakeLauncher in tests).
type Launcher interface {
	Launch(ctx context.Context, req LaunchRequest) (LaunchResult, error)
}

// NoopLauncher creates no task: the run stays queued with no task_id.
type NoopLauncher struct{}

func (NoopLauncher) Launch(context.Context, LaunchRequest) (LaunchResult, error) {
	return LaunchResult{}, nil
}

// FakeLauncher records launches and hands out fake task ids (tests). Err,
// when set, fails every launch.
type FakeLauncher struct {
	mu       sync.Mutex
	Launches []LaunchRequest
	Err      error
	n        int
}

func (f *FakeLauncher) Launch(_ context.Context, req LaunchRequest) (LaunchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return LaunchResult{}, f.Err
	}
	f.n++
	f.Launches = append(f.Launches, req)
	return LaunchResult{TaskID: "task-fake-" + req.Run.ID, SessionID: "sess-fake-" + req.Run.ID}, nil
}

// Requests returns a copy of the recorded launches.
func (f *FakeLauncher) Requests() []LaunchRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]LaunchRequest(nil), f.Launches...)
}

// Messenger sends a chat message to a run's agent (Host Messages().Send).
type Messenger interface {
	Send(ctx context.Context, taskID, sessionID, text string) error
}

// NoopMessenger drops messages (tests, or no Host yet).
type NoopMessenger struct{}

func (NoopMessenger) Send(context.Context, string, string, string) error { return nil }

// ProfileLister lists the Kandev agent profiles runs may use.
type ProfileLister func(ctx context.Context) ([]contract.AgentProfile, error)

// Service ties the store and hub together.
type Service struct {
	Store     *store.Store
	Hub       *hub.Hub
	Launcher  Launcher
	Messenger Messenger
	// Stops is the Phase 2a hook for the kill switch. See StopAllRuns.
	Stops StopAllRuns
	Logf  func(format string, args ...any)

	// Profiles resolves a run's profile id or name to its id, name and
	// harness. When nil (tests), runs keep the profile fields they were given.
	Profiles ProfileLister
	// DefaultProfile is the plugin-config default profile ("" for none).
	DefaultProfile func(ctx context.Context) string
	// Outline renders a run's `context` text for prompts of harnesses that
	// may have no tools (the bridge prompt's outline).
	Outline func(r store.RunRecord) string
	// OnTaskBound is called once a run's task id is recorded, so events that
	// arrived before it can be replayed.
	OnTaskBound func(taskID string)

	// submitMu orders commit+broadcast so every client sees ops in rev
	// order and before its own ack.
	submitMu  sync.Mutex
	runMu     sync.Mutex
	heldMu    sync.Mutex
	heldRepos map[string]string // run id -> repo of runs held for approval

	mu         sync.Mutex
	port       int
	origins    map[string]bool
	lastOutput map[string]time.Time
}

// New builds a service with no-op Kandev side effects.
func New(st *store.Store, h *hub.Hub) *Service {
	return &Service{Store: st, Hub: h, Launcher: NoopLauncher{}, Messenger: NoopMessenger{}, Logf: log.Printf,
		origins: map[string]bool{}, lastOutput: map[string]time.Time{}}
}

// SetPort records the loopback port (bridge tokens embed it).
func (s *Service) SetPort(p int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.port = p
}

// Port is the loopback port, or 0 before the server starts.
func (s *Service) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

// AddOrigin allows a browser origin (the Kandev UI origin seen by the
// session webhook) for CORS and WS upgrades.
func (s *Service) AddOrigin(o string) {
	o = strings.TrimRight(strings.TrimSpace(o), "/")
	if o == "" || o == "null" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.origins[o] = true
}

// OriginAllowed reports whether a browser origin may use local tokens:
// recorded Kandev origins and loopback/desktop-shell origins.
func (s *Service) OriginAllowed(o string) bool {
	if o == "" {
		return true // non-browser client
	}
	s.mu.Lock()
	ok := s.origins[o]
	s.mu.Unlock()
	if ok {
		return true
	}
	for _, p := range []string{"http://127.0.0.1", "http://localhost", "http://[::1]", "tauri://localhost", "http://tauri.localhost", "https://tauri.localhost"} {
		if o == p || strings.HasPrefix(o, p+":") {
			return true
		}
	}
	return false
}

// SubmitOptions are per-call options of Submit.
type SubmitOptions struct {
	Actor        contract.Actor
	OriginConn   string // WS connection id: excluded from the ops broadcast
	OriginClient string // hello client_id: excluded from long-poll echoes
	BatchID      string
	NoLocks      bool
}

// Submit commits ops and broadcasts them.
func (s *Service) Submit(mapID string, ops []contract.Op, o SubmitOptions) (store.SubmitResult, error) {
	s.submitMu.Lock()
	defer s.submitMu.Unlock()
	opt := store.SubmitOptions{Actor: o.Actor, BatchID: o.BatchID}
	if !o.NoLocks {
		opt.Locked = s.Hub.Locked(mapID)
	}
	res, err := s.Store.Submit(mapID, ops, opt)
	if err != nil {
		return res, err
	}
	s.afterCommit(mapID, res.Committed, res.Rev, o.OriginConn, o.OriginClient)
	return res, nil
}

// Undo reverts the participant's latest batch and broadcasts the inverse.
func (s *Service) Undo(mapID string, actor contract.Actor) (store.SubmitResult, error) {
	s.submitMu.Lock()
	defer s.submitMu.Unlock()
	res, err := s.Store.Undo(mapID, actor.ParticipantID, store.SubmitOptions{Actor: actor, Locked: s.Hub.Locked(mapID)})
	if err != nil {
		return res, err
	}
	s.afterCommit(mapID, res.Committed, res.Rev, "", "")
	return res, nil
}

// Review resolves a proposal and broadcasts applied ops and the proposal.
func (s *Service) Review(proposalID string, req contract.ReviewProposalRequest, reviewer string) (contract.Proposal, error) {
	s.submitMu.Lock()
	defer s.submitMu.Unlock()
	res, err := s.Store.ReviewProposal(proposalID, req, reviewer)
	if err != nil {
		return contract.Proposal{}, err
	}
	s.afterCommit(res.Proposal.MapID, res.Committed, res.Rev, "", "")
	s.Hub.Publish(res.Proposal.MapID, contract.Message{Type: contract.MsgProposal, Payload: &contract.ProposalPayload{Proposal: res.Proposal}})
	if res.Proposal.Status.Resolved() {
		s.Activity(contract.ActivityEvent{MapID: res.Proposal.MapID, Type: contract.ActivityProposalResolved,
			Actor: contract.Actor{ParticipantID: reviewer}, Summary: "Proposal " + string(res.Proposal.Status), Count: len(res.Proposal.Items), Rev: res.Rev})
	}
	return res.Proposal, nil
}

// Propose stores ops in a proposal and broadcasts the updated proposal.
func (s *Service) Propose(proposalID string, ops []contract.Op, actor contract.Actor) (contract.Proposal, []contract.Op, error) {
	out, err := s.Store.Propose(proposalID, ops, actor)
	if err != nil {
		return contract.Proposal{}, nil, err
	}
	p, err := s.Store.GetProposal(proposalID)
	if err != nil {
		return p, out, err
	}
	s.Hub.Publish(p.MapID, contract.Message{Type: contract.MsgProposal, Payload: &contract.ProposalPayload{Proposal: p}})
	return p, out, nil
}

// afterCommit broadcasts committed ops and runs side effects. Caller holds
// submitMu.
func (s *Service) afterCommit(mapID string, ops []contract.Op, rev int64, conn, client string) {
	if len(ops) == 0 {
		return
	}
	s.Hub.PublishOps(mapID, ops, rev, conn, client)
	for _, o := range ops {
		if p, ok := o.Payload.(*contract.QuestionAnswerPayload); ok {
			go s.relayAnswer(mapID, p, o.Actor)
		}
	}
}

// relayAnswer sends an ask_human answer back to the asking run's agent.
func (s *Service) relayAnswer(mapID string, p *contract.QuestionAnswerPayload, by contract.Actor) {
	n, err := s.Store.Node(mapID, p.NodeID)
	if err != nil || n.Question == nil || n.Question.RunID == "" {
		return
	}
	run, err := s.Store.GetRun(n.Question.RunID)
	if err != nil || run.TaskID == "" {
		return
	}
	name := "A human"
	if pt, err := s.Store.GetParticipant(by.ParticipantID); err == nil {
		name = pt.Name
	}
	text := "Answer to your mind map question [" + n.ID + "] \"" + n.Title + "\" from " + name + ":\n\n" + p.Answer
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.Messenger.Send(ctx, run.TaskID, run.SessionID, text); err != nil {
		s.Logf("mindmap: relay answer to run %s: %v", run.ID, err)
	} else if open, _ := s.Store.OpenQuestions(mapID, run.ID); !open {
		// The answer restarts the agent. If it is still mid-turn no new
		// session state arrives, so leave waiting(question) here.
		if r, err := s.UpdateRun(run.ID, func(r *store.RunRecord) bool {
			if r.Status != contract.RunWaiting || r.WaitingReason != contract.WaitingQuestion {
				return false
			}
			r.Status, r.WaitingReason = contract.RunRunning, ""
			return true
		}); err == nil {
			s.SetRunNodeStatus(r)
		}
	}
	s.Activity(contract.ActivityEvent{MapID: mapID, Type: contract.ActivityQuestionAnswered, Actor: by,
		Summary: name + " answered \"" + n.Title + "\"", NodeIDs: []string{n.ID}})
}

// Activity stores and broadcasts a feed event.
func (s *Service) Activity(ev contract.ActivityEvent) {
	ev, err := s.Store.AddActivity(ev)
	if err != nil {
		s.Logf("mindmap: activity: %v", err)
		return
	}
	s.Hub.Publish(ev.MapID, contract.Message{Type: contract.MsgActivity, Payload: &contract.ActivityPayload{Events: []contract.ActivityEvent{ev}}})
}

// PublishRun broadcasts a run's current state.
func (s *Service) PublishRun(r store.RunRecord) {
	s.Hub.Publish(r.MapID, contract.Message{Type: contract.MsgRun, Payload: &contract.RunPayload{Run: r.Run}})
}

// SaveRun persists and broadcasts a run.
func (s *Service) SaveRun(r store.RunRecord) (store.RunRecord, error) {
	r, err := s.Store.SaveRun(r)
	if err != nil {
		return r, err
	}
	s.PublishRun(r)
	return r, nil
}

// Snapshot is the store snapshot plus live presence and online flags.
func (s *Service) Snapshot(mapID string) (contract.Snapshot, error) {
	snap, err := s.Store.Snapshot(mapID)
	if err != nil {
		return snap, err
	}
	s.Hub.SetRev(mapID, snap.Rev)
	snap.Presence = s.Hub.Presence(mapID)
	online := s.Hub.Online(mapID)
	for i := range snap.Participants {
		snap.Participants[i].Online = online[snap.Participants[i].ID]
	}
	return snap, nil
}

// SetRunNodeStatus moves the ring of a run's node, as the run's agent.
func (s *Service) SetRunNodeStatus(r store.RunRecord) {
	st := contract.NodeStatusForRun(r.Status)
	n, err := s.Store.Node(r.MapID, r.NodeID)
	if err != nil || n.Deleted || (n.Status == st && n.RunID == r.ID) {
		return
	}
	runID := r.ID
	op := contract.Op{ID: store.NewID(contract.PrefixOp), MapID: r.MapID, Type: contract.OpNodeUpdate,
		Payload: &contract.NodeUpdatePayload{ID: r.NodeID, Set: &contract.NodeFields{Status: &st, RunID: &runID}}}
	if _, err := s.Submit(r.MapID, []contract.Op{op}, SubmitOptions{Actor: contract.Actor{ParticipantID: r.ParticipantID, RunID: r.ID}, NoLocks: true}); err != nil {
		s.Logf("mindmap: node status for run %s: %v", r.ID, err)
	}
}

// RecordOutput publishes agent output for subscribers and updates the run's
// last_output at most once a second.
func (s *Service) RecordOutput(r store.RunRecord, lines []contract.OutputLine) {
	if len(lines) == 0 {
		return
	}
	s.Hub.PublishOutput(r.MapID, r.ID, lines)
	s.mu.Lock()
	last := s.lastOutput[r.ID]
	now := time.Now()
	due := now.Sub(last) >= time.Second
	if due {
		s.lastOutput[r.ID] = now
	}
	s.mu.Unlock()
	if !due {
		return
	}
	text := strings.TrimSpace(lines[len(lines)-1].Text)
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		text = strings.TrimSpace(text[i+1:])
	}
	if len(text) > 300 {
		text = text[:300]
	}
	if text == "" || text == r.LastOutput {
		return
	}
	if _, err := s.UpdateRun(r.ID, func(cur *store.RunRecord) bool {
		if cur.LastOutput == text {
			return false
		}
		cur.LastOutput = text
		return true
	}); err != nil {
		s.Logf("mindmap: save last output: %v", err)
	}
}

// UpdateRun does a serialised read-modify-write of one run. fn returns false
// to skip saving. The saved run is broadcast, and a swarm coordinator above
// it recomputes its status.
func (s *Service) UpdateRun(runID string, fn func(r *store.RunRecord) bool) (store.RunRecord, error) {
	r, saved, err := s.updateRun(runID, fn)
	if err == nil && saved && r.ParentRunID != "" {
		s.syncCoordinator(r.ParentRunID)
	}
	return r, err
}

func (s *Service) updateRun(runID string, fn func(r *store.RunRecord) bool) (store.RunRecord, bool, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	r, err := s.Store.GetRun(runID)
	if err != nil {
		return r, false, err
	}
	if !fn(&r) {
		return r, false, nil
	}
	r, err = s.SaveRun(r)
	return r, err == nil, err
}

// SnapshotWithCursor returns a snapshot and the long-poll cursor of the same
// instant (no op can commit in between).
func (s *Service) SnapshotWithCursor(mapID string) (contract.Snapshot, string, error) {
	s.submitMu.Lock()
	defer s.submitMu.Unlock()
	snap, err := s.Snapshot(mapID)
	if err != nil {
		return snap, "", err
	}
	return snap, s.Hub.Cursor(mapID), nil
}

// Join queues welcome plus a snapshot (or a catch-up `ops` message from
// sinceRev) for c and registers it with the hub, atomically with respect to
// commits, so c receives every later op exactly once and in order.
func (s *Service) Join(c *hub.Client, sinceRev int64, maxCatchUp int64) error {
	s.submitMu.Lock()
	defer s.submitMu.Unlock()
	m, err := s.Store.GetMap(c.MapID)
	if err != nil {
		return err
	}
	s.Hub.SetRev(c.MapID, m.Rev)
	c.Send(contract.Message{Type: contract.MsgWelcome, Payload: &contract.WelcomePayload{Contract: contract.ContractVersion,
		Participant: c.Participant, ServerTime: time.Now().UTC(), Cursor: s.Hub.Cursor(c.MapID)}})
	if sinceRev > 0 && sinceRev <= m.Rev && m.Rev-sinceRev <= maxCatchUp {
		ops, err := s.Store.OpsSince(c.MapID, sinceRev, 0)
		if err != nil {
			return err
		}
		c.Send(contract.Message{Type: contract.MsgOps, Payload: &contract.OpsPayload{Ops: ops, Rev: m.Rev}})
	} else {
		snap, err := s.Snapshot(c.MapID)
		if err != nil {
			return err
		}
		c.Send(contract.Message{Type: contract.MsgSnapshot, Payload: &snap})
	}
	s.Hub.Join(c)
	return nil
}
