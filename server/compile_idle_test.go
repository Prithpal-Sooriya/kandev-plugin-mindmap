package main

import (
	"strings"
	"testing"

	"kandev-plugin-mindmap/server/app"
	"kandev-plugin-mindmap/server/contract"
)

// idleCompile starts a Pi compile of a two-branch map. Both section agents
// are running; the first has reported done.
func idleCompile(t *testing.T) (*mindmapPlugin, *fakeHost, []contract.Run) {
	t.Helper()
	p, h := newTestPluginHost(t)
	h.config["export_dir"] = t.TempDir()
	m := newMapViaAPI(t, p)
	branches := addKids(t, p, m, m.RootID, "", "Food", "Games")
	addKids(t, p, m, branches[0], "", "Sandwiches")
	addKids(t, p, m, branches[1], "", "Frisbee")
	runs := apiRuns(t, p, "/api/maps/"+m.ID+"/compile", map[string]any{"node_id": m.RootID, "profile_id": "prof-pi", "target": "dir"})
	if len(runs) != 3 {
		t.Fatalf("compile runs: %d", len(runs))
	}
	for _, r := range runs[1:] {
		state(p, r.TaskID, "RUNNING")
	}
	tool(t, p, runs[1].TaskID, "report", map[string]any{"summary": "Wrote Food.", "status": "done"})
	state(p, runs[1].TaskID, "IDLE")
	if r := getRun(t, p, runs[1].ID); r.Status != contract.RunDone {
		t.Fatalf("reported section: %s", r.Status)
	}
	return p, h, runs
}

func sentTo(h *fakeHost, taskID string) []string {
	var out []string
	for _, s := range h.Sent() {
		if s.TaskID == taskID {
			out = append(out, s.Text)
		}
	}
	return out
}

func TestCompileIdleSectionDoesNotStartSynthesis(t *testing.T) {
	p, h, runs := idleCompile(t)
	games := runs[2]
	state(p, games.TaskID, "IDLE")
	state(p, games.TaskID, "WAITING_FOR_INPUT") // still the same pause
	if r := getRun(t, p, games.ID); r.Status != contract.RunWaiting || r.WaitingReason != contract.WaitingTurnEnded {
		t.Fatalf("idle section: %s/%s", r.Status, r.WaitingReason)
	}
	if len(h.Created()) != 2 {
		t.Fatalf("synthesis started while a section was idle without a report: %d tasks", len(h.Created()))
	}
	msgs := sentTo(h, games.TaskID)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "write_doc") || !strings.Contains(msgs[0], "`report`") || !strings.Contains(msgs[0], "games/index.md") {
		t.Fatalf("continuation messages: %q", msgs)
	}
	if len(sentTo(h, runs[1].TaskID)) != 0 {
		t.Fatal("the reported section was nudged")
	}
	if c := getRun(t, p, runs[0].ID); c.Status != contract.RunRunning {
		t.Fatalf("coordinator: %s", c.Status)
	}
}

func TestCompileSectionReportAfterNudgeStartsSynthesis(t *testing.T) {
	p, h, runs := idleCompile(t)
	games := runs[2]
	state(p, games.TaskID, "IDLE")
	state(p, games.TaskID, "RUNNING") // woken by the nudge
	tool(t, p, games.TaskID, "report", map[string]any{"summary": "Wrote Games.", "status": "done"})
	state(p, games.TaskID, "IDLE")
	if len(h.Created()) != 3 {
		t.Fatalf("synthesis not started after the section reported: %d tasks", len(h.Created()))
	}
	if n := len(sentTo(h, games.TaskID)); n != 1 {
		t.Fatalf("section got %d continuation messages, want 1", n)
	}
	// An idle synthesis is nudged too, and the compile is not done yet.
	synth, err := p.store.RunByTask("task-3")
	if err != nil || synth.Template != contract.TemplateSynthesis {
		t.Fatalf("synthesis run: %+v %v", synth, err)
	}
	state(p, synth.TaskID, "RUNNING")
	state(p, synth.TaskID, "IDLE")
	if c := getRun(t, p, runs[0].ID); c.Status != contract.RunRunning {
		t.Fatalf("coordinator with an idle synthesis: %s", c.Status)
	}
	if msgs := sentTo(h, synth.TaskID); len(msgs) != 1 || !strings.Contains(msgs[0], "single doc") {
		t.Fatalf("synthesis continuation messages: %q", msgs)
	}
	state(p, synth.TaskID, "RUNNING")
	tool(t, p, synth.TaskID, "report", map[string]any{"summary": "Wrote the overview.", "status": "done"})
	state(p, synth.TaskID, "IDLE")
	if c := getRun(t, p, runs[0].ID); c.Status != contract.RunDone {
		t.Fatalf("coordinator after the synthesis reported: %s (%s)", c.Status, c.Error)
	}
}

func TestCompileSectionFailsAfterTwoNudges(t *testing.T) {
	p, h, runs := idleCompile(t)
	games := runs[2]
	for i := range app.MaxCompileNudges {
		state(p, games.TaskID, "IDLE")
		if len(h.Created()) != 2 {
			t.Fatalf("synthesis started after idle %d", i+1)
		}
		state(p, games.TaskID, "RUNNING")
	}
	state(p, games.TaskID, "IDLE") // idle again after the last nudge
	r := getRun(t, p, games.ID)
	if r.Status != contract.RunFailed || !strings.Contains(r.Error, app.IdleFailure) {
		t.Fatalf("section after %d nudges: %s %q", app.MaxCompileNudges, r.Status, r.Error)
	}
	if n := len(sentTo(h, games.TaskID)); n != app.MaxCompileNudges {
		t.Fatalf("continuation messages: %d, want %d", n, app.MaxCompileNudges)
	}
	if len(h.Created()) != 3 {
		t.Fatalf("synthesis not started after the idle section failed: %d tasks", len(h.Created()))
	}
	if sp := prompt(h, 2); !strings.Contains(sp, "its agent failed") {
		t.Fatalf("synthesis prompt does not flag the failed section:\n%s", sp)
	}
}
