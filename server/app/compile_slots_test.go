package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/store"
)

func (e *lifecycleEnv) addBranches(t *testing.T, titles ...string) {
	t.Helper()
	last := ""
	var ops []contract.Op
	for _, title := range titles {
		last = contract.OrderBetween(last, "")
		ops = append(ops, contract.Op{ID: store.NewID(contract.PrefixOp), MapID: e.m.ID, Type: contract.OpNodeCreate, Payload: &contract.NodeCreatePayload{
			Node: contract.NodeInit{ID: store.NewID(contract.PrefixNode), ParentID: e.m.RootID, Order: last, Title: title, BodyMD: "a few words", Kind: contract.KindIdea}}})
	}
	if _, err := e.svc.Submit(e.m.ID, ops, SubmitOptions{Actor: contract.Actor{ParticipantID: e.host.ID}}); err != nil {
		t.Fatal(err)
	}
}

func TestCompileReservesRunSlots(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	e.settings(t, contract.MapSettings{MaxConcurrentRun: 2})
	e.addBranches(t, "Food", "Games", "Weather", "Transport")
	e.svc.Profiles = func(context.Context) ([]contract.AgentProfile, error) {
		return []contract.AgentProfile{{ID: "prof-pi", Name: "Pi", Harness: contract.HarnessOther}}, nil
	}

	// Every launch checks the map, and the first one also tries to start an
	// unrelated run while the compile's second slot is reserved but its row
	// does not exist yet: the cap must refuse it.
	var mu sync.Mutex
	peak, sections, synths := 0, 0, 0
	var outside error
	tried := false
	e.lnch.during = func(req LaunchRequest) {
		mu.Lock()
		defer mu.Unlock()
		if req.Run.Template == contract.TemplateResearch {
			return
		}
		kids, err := e.st.ChildRuns(req.Run.ParentRunID)
		if err != nil {
			t.Error(err)
			return
		}
		live := 0
		for _, k := range kids {
			if !k.Status.Terminal() {
				live++
			}
		}
		peak = max(peak, live)
		if active, _ := e.st.CountActiveRuns(e.m.ID); active > 2 {
			t.Errorf("%d runs active on a map capped at 2", active)
		}
		switch req.Run.Template {
		case contract.TemplateCompile:
			sections++
		case contract.TemplateSynthesis:
			synths++
		}
		if !tried {
			tried = true
			mu.Unlock()
			_, outside = e.svc.StartRun(ctx, RunSpec{NewRun: store.NewRun{MapID: e.m.ID, NodeID: e.m.RootID, ProfileID: "prof-pi",
				Template: contract.TemplateResearch, StartedBy: e.host.ID}})
			mu.Lock()
		}
	}

	runs, err := e.svc.StartCompile(ctx, CompileSpec{MapID: e.m.ID, ProfileID: "prof-pi", Target: "dir", StartedBy: e.host.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("cap 2 with 4 sections: first wave %d runs, want coordinator and 2 sections", len(runs))
	}
	if outside == nil || contract.AsError(outside).Code != contract.ErrBudgetExceeded {
		t.Fatalf("a run started while the compile held both slots: %v", outside)
	}
	coord := runs[0]

	// Finish section runs one at a time; each frees a slot for the next.
	finish := func(d time.Duration) {
		t.Helper()
		kids, err := e.st.ChildRuns(coord.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range kids {
			if !k.Status.Terminal() {
				e.setStatus(t, k.ID, d, contract.RunDone, "")
				return
			}
		}
		t.Fatal("no run left to finish")
	}
	for i := range 4 {
		finish(time.Duration(i+1) * time.Minute)
	}
	mu.Lock()
	if sections != 4 || synths != 1 {
		t.Fatalf("started %d sections and %d synthesis runs, want 4 and 1", sections, synths)
	}
	if peak > 2 {
		t.Fatalf("%d compile runs active at once on a map capped at 2", peak)
	}
	mu.Unlock()
	finish(5 * time.Minute)
	if got := e.status(t, coord.ID); got.Status != contract.RunDone {
		t.Fatalf("compile after its synthesis finished: %s %q", got.Status, got.Error)
	}
	if len(e.svc.pendingRuns) != 0 {
		t.Fatalf("leaked reservations: %v", e.svc.pendingRuns)
	}
}

func TestCompileWaitsForSlotHeldByOtherRun(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	e.settings(t, contract.MapSettings{MaxConcurrentRun: 2})
	e.addBranches(t, "Food", "Games")
	other := e.startRun(t, 30)
	runs, err := e.svc.StartCompile(ctx, CompileSpec{MapID: e.m.ID, Target: "dir", StartedBy: e.host.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("one slot free: first wave %d runs, want coordinator and 1 section", len(runs))
	}
	coord := runs[0]
	plan, _ := e.svc.loadPlan(coord.ID)
	if plan.Sections[1].RunID != "" || plan.Sections[1].Error != "" {
		t.Fatalf("second section should wait for a slot: %+v", plan.Sections[1])
	}
	// The unrelated run ends: its slot goes to the waiting section.
	e.setStatus(t, other.ID, time.Minute, contract.RunDone, "")
	plan, _ = e.svc.loadPlan(coord.ID)
	if plan.Sections[1].RunID == "" {
		t.Fatalf("second section did not start when a slot freed: %+v", plan.Sections[1])
	}
	if e.lnch.count() != 3 {
		t.Fatalf("launches: %d, want the other run and 2 sections", e.lnch.count())
	}
}
