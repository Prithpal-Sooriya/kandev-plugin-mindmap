package app

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/hub"
	"kandev-plugin-mindmap/server/store"
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

type sent struct{ taskID, text string }

type testMessenger struct {
	mu   sync.Mutex
	sent []sent
}

func (m *testMessenger) Send(_ context.Context, taskID, _, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sent{taskID, text})
	return nil
}

func (m *testMessenger) to(taskID string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, s := range m.sent {
		if s.taskID == taskID {
			out = append(out, s.text)
		}
	}
	return out
}

// testLauncher gives each run the task "task-<run id>" and calls during,
// if set, while the task is being created.
type testLauncher struct {
	mu     sync.Mutex
	calls  []LaunchRequest
	during func(req LaunchRequest)
}

func (l *testLauncher) Launch(_ context.Context, req LaunchRequest) (LaunchResult, error) {
	l.mu.Lock()
	l.calls = append(l.calls, req)
	during := l.during
	l.mu.Unlock()
	if during != nil {
		during(req)
	}
	return LaunchResult{TaskID: "task-" + req.Run.ID, SessionID: "sess-" + req.Run.ID}, nil
}

func (l *testLauncher) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.calls)
}

type lifecycleEnv struct {
	svc   *Service
	st    *store.Store
	clk   *testClock
	msgs  *testMessenger
	lnch  *testLauncher
	m     contract.Map
	host  contract.Participant
	start time.Time
}

func newLifecycleEnv(t *testing.T) *lifecycleEnv {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	t0 := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	clk := &testClock{t: t0}
	st.Now = clk.Now
	m, host, err := st.CreateMap(contract.CreateMapRequest{WorkspaceID: "ws-1", Title: "Lifecycle"})
	if err != nil {
		t.Fatal(err)
	}
	e := &lifecycleEnv{svc: New(st, hub.New()), st: st, clk: clk, msgs: &testMessenger{}, lnch: &testLauncher{}, m: m, host: host, start: t0}
	e.svc.Messenger, e.svc.Launcher = e.msgs, e.lnch
	e.svc.Logf = t.Logf
	return e
}

func (e *lifecycleEnv) at(d time.Duration) time.Time { return e.start.Add(d) }

func (e *lifecycleEnv) settings(t *testing.T, ms contract.MapSettings) {
	t.Helper()
	op := contract.Op{ID: store.NewID(contract.PrefixOp), MapID: e.m.ID, Type: contract.OpMapUpdate, Payload: &contract.MapUpdatePayload{Settings: &ms}}
	if _, err := e.svc.Submit(e.m.ID, []contract.Op{op}, SubmitOptions{Actor: contract.Actor{ParticipantID: e.host.ID}}); err != nil {
		t.Fatal(err)
	}
}

func (e *lifecycleEnv) startRun(t *testing.T, minutes int) store.RunRecord {
	t.Helper()
	r, err := e.svc.StartRun(context.Background(), RunSpec{NewRun: store.NewRun{MapID: e.m.ID, NodeID: e.m.RootID, Template: contract.TemplateResearch,
		StartedBy: e.host.ID, Budget: &contract.Budget{MaxMinutes: minutes}}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// setStatus moves a run at clock time d, as session events would.
func (e *lifecycleEnv) setStatus(t *testing.T, id string, d time.Duration, st contract.RunStatus, wr contract.WaitingReason) {
	t.Helper()
	e.clk.Set(e.at(d))
	if _, err := e.svc.UpdateRun(id, func(r *store.RunRecord) bool {
		r.Status, r.WaitingReason = st, wr
		return true
	}); err != nil {
		t.Fatal(err)
	}
}

func (e *lifecycleEnv) tick(d time.Duration) {
	e.clk.Set(e.at(d))
	e.svc.EnforceTimeBudgets(context.Background(), e.at(d))
}

func (e *lifecycleEnv) status(t *testing.T, id string) store.RunRecord {
	t.Helper()
	r, err := e.st.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTimeBudgetCountsOnlyQueuedAndRunningTime(t *testing.T) {
	e := newLifecycleEnv(t)
	r := e.startRun(t, 5)
	e.setStatus(t, r.ID, 0, contract.RunRunning, "")
	e.tick(time.Minute)
	e.setStatus(t, r.ID, 3*time.Minute, contract.RunWaiting, contract.WaitingQuestion)
	e.tick(3*time.Minute + 30*time.Second)
	e.tick(13 * time.Minute) // ten idle minutes do not count
	if got := e.status(t, r.ID); got.Status.Terminal() {
		t.Fatalf("stopped while waiting on a human: %s %q", got.Status, got.Error)
	}
	e.setStatus(t, r.ID, 13*time.Minute+30*time.Second, contract.RunRunning, "")
	e.tick(14 * time.Minute)
	e.tick(15 * time.Minute) // 3 + 1.5 minutes used
	if got := e.status(t, r.ID); got.Status.Terminal() {
		t.Fatalf("stopped after 4.5 active minutes: %s %q", got.Status, got.Error)
	}
	e.tick(16 * time.Minute) // 5.5 minutes used
	got := e.status(t, r.ID)
	if got.Status != contract.RunStopped || !strings.Contains(got.Error, "time budget") {
		t.Fatalf("after 5.5 active minutes: %s %q", got.Status, got.Error)
	}
}

func TestMessageToIdleRunStartsNewTimeBudget(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	review := e.startRun(t, 5)
	done := e.startRun(t, 5)
	stopped := e.startRun(t, 5)
	failed := e.startRun(t, 5)
	for _, r := range []store.RunRecord{review, done, stopped, failed} {
		e.setStatus(t, r.ID, 0, contract.RunRunning, "")
	}
	e.tick(time.Minute)
	e.setStatus(t, review.ID, 2*time.Minute, contract.RunReview, "")
	e.setStatus(t, done.ID, 2*time.Minute, contract.RunDone, "")
	e.setStatus(t, failed.ID, 2*time.Minute, contract.RunFailed, "")
	if _, err := e.svc.StopRun(ctx, stopped.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	e.tick(2*time.Minute + 30*time.Second)
	e.tick(60 * time.Minute)
	if got := e.status(t, review.ID); got.Status != contract.RunReview {
		t.Fatalf("idle review run was stopped: %s", got.Status)
	}
	if s := e.msgs.to(review.TaskID); len(s) != 0 {
		t.Fatalf("idle review run was messaged: %q", s)
	}

	e.clk.Set(e.at(60 * time.Minute))
	for _, r := range []store.RunRecord{review, done} {
		if err := e.svc.MessageRun(ctx, r.ID, e.host.ID, "One more thing."); err != nil {
			t.Fatal(err)
		}
	}
	e.tick(60*time.Minute + 30*time.Second) // not woken yet
	e.setStatus(t, review.ID, 61*time.Minute, contract.RunRunning, "")
	e.setStatus(t, done.ID, 61*time.Minute, contract.RunRunning, "")
	e.tick(61*time.Minute + 30*time.Second)
	e.tick(64 * time.Minute)
	for _, r := range []store.RunRecord{review, done} {
		if got := e.status(t, r.ID); got.Status != contract.RunRunning {
			t.Fatalf("woken run stopped inside its new window: %s %q", got.Status, got.Error)
		}
	}
	e.tick(65*time.Minute + 30*time.Second) // 5.5 minutes since the message
	for _, r := range []store.RunRecord{review, done} {
		got := e.status(t, r.ID)
		if got.Status != contract.RunStopped || !strings.Contains(got.Error, "time budget") {
			t.Fatalf("woken run past its new window: %s %q", got.Status, got.Error)
		}
	}

	if s := e.msgs.to(stopped.TaskID); len(s) != 1 {
		t.Fatalf("stopped run was told to stop %d times", len(s))
	}
	if got := e.status(t, failed.ID); got.Status != contract.RunFailed || len(e.msgs.to(failed.TaskID)) != 0 {
		t.Fatalf("failed run was stopped: %s, %d messages", got.Status, len(e.msgs.to(failed.TaskID)))
	}
}

func TestParallelStartsRespectConcurrencyCap(t *testing.T) {
	e := newLifecycleEnv(t)
	e.svc.Launcher = NoopLauncher{}
	e.settings(t, contract.MapSettings{MaxConcurrentRun: 1})
	const n = 16
	// The profile lookup sits between the cap check and the insert. Hold
	// every start there until all have arrived (or none else can), so
	// starts that passed the check together would all insert.
	var arrivedMu sync.Mutex
	arrived := 0
	e.svc.Profiles = func(context.Context) ([]contract.AgentProfile, error) {
		arrivedMu.Lock()
		arrived++
		arrivedMu.Unlock()
		deadline := time.Now().Add(300 * time.Millisecond)
		for time.Now().Before(deadline) {
			arrivedMu.Lock()
			all := arrived == n
			arrivedMu.Unlock()
			if all {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		return []contract.AgentProfile{{ID: "prof-1", Name: "Claude", Harness: contract.HarnessClaude}}, nil
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	gate := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			_, errs[i] = e.svc.StartRun(context.Background(), RunSpec{NewRun: store.NewRun{MapID: e.m.ID, NodeID: e.m.RootID,
				ProfileID: "prof-1", Template: contract.TemplateResearch, StartedBy: e.host.ID}})
		}()
	}
	close(gate)
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case contract.AsError(err).Code != contract.ErrBudgetExceeded:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	active, err := e.st.CountActiveRuns(e.m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok != 1 || active != 1 {
		t.Fatalf("cap 1: %d starts succeeded, %d runs active", ok, active)
	}
	if len(e.svc.pendingRuns) != 0 {
		t.Fatalf("leaked reservations: %v", e.svc.pendingRuns)
	}
}

func TestStopWhileApprovedRunLaunches(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	e.settings(t, contract.MapSettings{RequireGuestRunApproval: true})
	guest, err := e.st.CreateGuest(e.m.ID, "Bea", "")
	if err != nil {
		t.Fatal(err)
	}
	held, err := e.svc.StartRun(ctx, RunSpec{NewRun: store.NewRun{MapID: e.m.ID, NodeID: e.m.RootID, Template: contract.TemplateResearch, StartedBy: guest.ID}})
	if err != nil || held.WaitingReason != contract.WaitingApproval {
		t.Fatalf("held run: %+v %v", held, err)
	}
	var token string
	e.lnch.during = func(req LaunchRequest) {
		token = req.RunToken
		if cur, _ := e.st.GetRun(req.Run.ID); cur.Status != contract.RunQueued {
			t.Errorf("launching a run in %s", cur.Status)
		}
		if _, err := e.svc.StopRun(ctx, req.Run.ID, e.host.ID, ""); err != nil {
			t.Error(err)
		}
	}
	runs, err := e.svc.ApproveRun(ctx, held.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("approve: %v %v", runs, err)
	}
	got := e.status(t, held.ID)
	if got.Status != contract.RunStopped || got.TaskID == "" {
		t.Fatalf("after a stop during launch: status %s, task %q", got.Status, got.TaskID)
	}
	if s := e.msgs.to(got.TaskID); len(s) != 1 || !strings.HasPrefix(s[0], StopMessage) {
		t.Fatalf("new task was not told to stop: %q", s)
	}
	if _, err := e.st.LookupToken(token); err == nil {
		t.Fatal("run token still valid after stop")
	}
}

func TestStoppedRunIsNotLaunched(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	run, token, err := e.st.CreateRun(store.NewRun{MapID: e.m.ID, NodeID: e.m.RootID, Template: contract.TemplateResearch, StartedBy: e.host.ID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StopRun(ctx, run.ID, e.host.ID, ""); err != nil {
		t.Fatal(err)
	}
	// A token minted after the stop (ApproveRun issues one after the flip
	// to queued) must not survive either.
	token2, err := e.st.IssueRunToken(run, 0)
	if err != nil {
		t.Fatal(err)
	}
	node, _ := e.st.Node(run.MapID, run.NodeID)
	got, err := e.svc.launch(ctx, run, node, token2, "")
	if err != nil {
		t.Fatal(err)
	}
	if e.lnch.count() != 0 || got.TaskID != "" || got.Status != contract.RunStopped {
		t.Fatalf("stopped run launched: %d launches, %+v", e.lnch.count(), got.Run)
	}
	for _, tk := range []string{token, token2} {
		if _, err := e.st.LookupToken(tk); err == nil {
			t.Fatal("run token still valid")
		}
	}
}

func TestSuggestStoppedBeforeProposeProposesNothing(t *testing.T) {
	e := newLifecycleEnv(t)
	e.svc.Suggest = func(context.Context, string, string) (string, error) {
		return `[{"title": "Alpha", "kind": "idea", "body": "a"}, {"title": "Beta", "kind": "risk", "body": "b"}]`, nil
	}
	prev := suggestBeforePropose
	t.Cleanup(func() { suggestBeforePropose = prev })
	suggestBeforePropose = func(runID string) {
		if _, err := e.svc.StopRun(context.Background(), runID, e.host.ID, ""); err != nil {
			t.Error(err)
		}
	}
	run, done, err := e.svc.StartSuggest(context.Background(), e.m.ID, e.m.RootID, 2, "", e.host.ID)
	if err != nil {
		t.Fatal(err)
	}
	<-done
	if got := e.status(t, run.ID); got.Status != contract.RunStopped {
		t.Fatalf("suggest run: %s %q", got.Status, got.Error)
	}
	p, err := e.st.GetProposal(run.ProposalID)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 0 {
		t.Fatalf("stopped suggest proposed %d items", len(p.Items))
	}
}
