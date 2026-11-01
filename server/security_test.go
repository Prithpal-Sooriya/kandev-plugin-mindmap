package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"kandev-plugin-mindmap/server/app"
	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/store"
)

// requireApproval turns on the guest-run hold and adds a guest to m.
func requireApproval(t *testing.T, p *mindmapPlugin, m contract.Map) contract.Participant {
	t.Helper()
	if st, b := api(t, p, "PUT", "/api/settings/share", map[string]any{"provider": "cloudflared", "require_guest_run_approval": true}); st != 200 {
		t.Fatalf("share settings: %d %s", st, b)
	}
	guest, err := p.store.CreateGuest(m.ID, "Bea", "")
	if err != nil {
		t.Fatal(err)
	}
	return guest
}

// concurrently calls fn from n goroutines released at once.
func concurrently(n int, fn func()) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			fn()
		}()
	}
	close(start)
	wg.Wait()
}

func TestConcurrentApproveLaunchesOnce(t *testing.T) {
	p, h := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	guest := requireApproval(t, p, m)
	ctx := context.Background()
	for i := range 10 {
		held, err := p.svc.StartRun(ctx, app.RunSpec{NewRun: store.NewRun{MapID: m.ID, NodeID: m.RootID, ProfileID: "prof-claude", StartedBy: guest.ID}})
		if err != nil || held.Status != contract.RunWaiting || held.WaitingReason != contract.WaitingApproval {
			t.Fatalf("held run: %+v %v", held.Run, err)
		}
		before := len(h.Created())
		var wins atomic.Int32
		concurrently(8, func() {
			if rs, err := p.svc.ApproveRun(ctx, held.ID); err == nil && len(rs) == 1 {
				wins.Add(1)
			} else if err != nil && contract.AsError(err).Code != contract.ErrConflict {
				t.Errorf("approve: %v", err)
			}
		})
		if n := len(h.Created()) - before; n != 1 || wins.Load() != 1 {
			t.Fatalf("round %d: %d tasks launched, %d approvals won", i, n, wins.Load())
		}
		if r := getRun(t, p, held.ID); r.TaskID == "" || r.Status != contract.RunQueued {
			t.Fatalf("approved run: %+v", r.Run)
		}
	}

	// Approving a swarm coordinator concurrently launches each lens once.
	runs, err := p.svc.StartSwarm(ctx, app.SwarmSpec{MapID: m.ID, NodeID: m.RootID, StartedBy: guest.ID,
		Agents: []contract.SwarmMember{{Lens: contract.LensResearcher, ProfileID: "prof-claude"}, {Lens: contract.LensSkeptic, ProfileID: "prof-claude"}}})
	if err != nil || len(runs) != 3 {
		t.Fatalf("guest swarm: %d %v", len(runs), err)
	}
	before := len(h.Created())
	concurrently(8, func() { _, _ = p.svc.ApproveRun(ctx, runs[0].ID) })
	if n := len(h.Created()) - before; n != 2 {
		t.Fatalf("swarm approvals launched %d tasks, want 2", n)
	}
}

// The api webhook cuts its request context at about 25 s; a launch that is
// under way must not inherit that cancellation.
func TestApproveLaunchIgnoresRequestCancel(t *testing.T) {
	p, h := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	guest := requireApproval(t, p, m)
	held, err := p.svc.StartRun(context.Background(), app.RunSpec{NewRun: store.NewRun{MapID: m.ID, NodeID: m.RootID, ProfileID: "prof-claude", StartedBy: guest.ID}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rs, err := p.svc.ApproveRun(ctx, held.ID)
	if err != nil || len(rs) != 1 || rs[0].TaskID == "" {
		t.Fatalf("approve with a canceled request: %+v %v", rs, err)
	}
	h.mu.Lock()
	errs := append([]error(nil), h.createCtxErrs...)
	h.mu.Unlock()
	if len(errs) != 1 || errs[0] != nil {
		t.Fatalf("Create saw a canceled context: %v", errs)
	}
}

// Stopping a swarm or compile while its loop is still launching children
// starts no further children.
func TestStopDuringLaunchStartsNoMoreChildren(t *testing.T) {
	ctx := context.Background()
	stopFirst := func(p *mindmapPlugin, h *fakeHost) {
		var once sync.Once
		h.onCreate = func(string) {
			once.Do(func() {
				coord, _ := h.Created()[0].Metadata["parent_run_id"].(string)
				if coord == "" {
					t.Error("child task has no parent_run_id")
					return
				}
				if _, err := p.svc.StopRun(ctx, coord, "", ""); err != nil {
					t.Error(err)
				}
			})
		}
	}

	t.Run("swarm", func(t *testing.T) {
		p, h := newTestPluginHost(t)
		m := newMapViaAPI(t, p)
		stopFirst(p, h)
		host, _ := p.store.HostParticipant(m.ID)
		runs, err := p.svc.StartSwarm(ctx, app.SwarmSpec{MapID: m.ID, NodeID: m.RootID, StartedBy: host.ID, Agents: []contract.SwarmMember{
			{Lens: contract.LensResearcher, ProfileID: "prof-claude"}, {Lens: contract.LensSkeptic, ProfileID: "prof-claude"},
			{Lens: contract.LensBuilder, ProfileID: "prof-claude"}}})
		if err != nil {
			t.Fatal(err)
		}
		if n := len(h.Created()); n != 1 {
			t.Fatalf("%d lens tasks launched after the swarm was stopped, want 1", n)
		}
		kids, _ := p.store.ChildRuns(runs[0].ID)
		for _, k := range kids {
			if k.Status != contract.RunStopped {
				t.Fatalf("lens run %s is %s", k.ID, k.Status)
			}
		}
	})

	t.Run("compile", func(t *testing.T) {
		p, h := newTestPluginHost(t)
		h.config["export_dir"] = t.TempDir()
		m, _ := compileMap(t, p)
		stopFirst(p, h)
		runs := apiRuns(t, p, "/api/maps/"+m.ID+"/compile", map[string]any{"node_id": m.RootID, "profile_id": "prof-claude"})
		if n := len(h.Created()); n != 1 {
			t.Fatalf("%d section tasks launched after the compile was stopped, want 1", n)
		}
		if c := getRun(t, p, runs[0].ID); c.Status != contract.RunStopped {
			t.Fatalf("coordinator: %s", c.Status)
		}
	})
}

// A guest (or anyone) messaging a run the host stopped must not revive it.
func TestMessageDoesNotReviveStoppedRun(t *testing.T) {
	p, h := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	guest, err := p.store.CreateGuest(m.ID, "Mallory", "")
	if err != nil {
		t.Fatal(err)
	}
	host, _ := p.store.HostParticipant(m.ID)
	ctx := context.Background()
	r := startRun(t, p, m, contract.TransportBridge)
	if _, err := p.svc.StopRun(ctx, r.ID, host.ID, ""); err != nil {
		t.Fatal(err)
	}
	sent := len(h.Sent())
	for _, by := range []string{guest.ID, host.ID} {
		if err := p.svc.MessageRun(ctx, r.ID, by, "keep going"); err == nil || contract.AsError(err).Code != contract.ErrConflict {
			t.Fatalf("message to a stopped run: %v", err)
		}
	}
	if got := getRun(t, p, r.ID); !got.StopRequested || got.Status != contract.RunStopped {
		t.Fatalf("stopped run revived: %s stop=%v", got.Status, got.StopRequested)
	}
	if len(h.Sent()) != sent {
		t.Fatalf("a message (with a fresh token) reached the stopped agent: %+v", h.Sent()[sent:])
	}

	failed := startRun(t, p, m, contract.TransportMCP)
	state(p, failed.TaskID, "FAILED")
	if err := p.svc.MessageRun(ctx, failed.ID, guest.ID, "retry"); err == nil || contract.AsError(err).Code != contract.ErrConflict {
		t.Fatalf("message to a failed run: %v", err)
	}
	// An open run can still be messaged.
	live := startRun(t, p, m, contract.TransportMCP)
	if err := p.svc.MessageRun(ctx, live.ID, guest.ID, "focus on B2B"); err != nil {
		t.Fatalf("message to a live run: %v", err)
	}
}

func TestAgentOutputRedactsTokens(t *testing.T) {
	p := newTestPlugin(t)
	m := newMapViaAPI(t, p)
	run := startRun(t, p, m, contract.TransportBridge)
	secret := "Abc_def-0123456789abcdefghijklmnopqrstuvwxy"
	line := `mindmap_add --token mmr1.38430.` + secret + ` --title "Pricing"`
	_ = p.OnEvent(context.Background(), event("message.added", map[string]any{"task_id": run.TaskID, "message_id": "m1",
		"author_type": "agent", "type": "tool_call", "content": line}))
	got := getRun(t, p, run.ID).LastOutput
	if strings.Contains(got, secret) || got != `mindmap_add --token mmr1.38430.[redacted] --title "Pricing"` {
		t.Fatalf("last output not redacted: %q", got)
	}
}

// Two maps compiled with the same slug must not overwrite each other's
// exported files.
func TestExportSlugIsScopedToMap(t *testing.T) {
	p, h := newTestPluginHost(t)
	dir := t.TempDir()
	h.config["export_dir"] = dir
	a := newMapViaAPI(t, p)
	b := newMapInWorkspace(t, p, "ws-1")
	for _, m := range []contract.Map{a, b} {
		apiRuns(t, p, "/api/maps/"+m.ID+"/compile", map[string]any{"node_id": m.RootID, "profile_id": "prof-claude", "slug": "shared", "target": "dir"})
		if st, body := api(t, p, "POST", "/api/maps/"+m.ID+"/export", nil); st != 200 {
			t.Fatalf("export %s: %d %s", m.ID, st, body)
		}
	}
	for m, title := range map[string]string{a.ID: "Product launch", b.ID: "Side project"} {
		got, err := os.ReadFile(filepath.Join(dir, m, "shared.md"))
		if err != nil || !strings.HasPrefix(string(got), "# "+title) {
			t.Fatalf("export of %s: %q %v", m, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "shared.md")); !os.IsNotExist(err) {
		t.Fatalf("export written outside the map folder: %v", err)
	}
}
