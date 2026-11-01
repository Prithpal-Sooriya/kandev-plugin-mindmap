package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"

	"kandev-plugin-mindmap/server/app"
	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/store"
)

// api calls one loopback /api route through the `api` webhook, as the host.
func api(t *testing.T, p *mindmapPlugin, method, path string, body any) (int32, []byte) {
	t.Helper()
	req := map[string]any{"method": method, "path": path}
	if body != nil {
		req["body"] = body
	}
	return webhook(t, p, "api", "POST", "", req)
}

func apiRuns(t *testing.T, p *mindmapPlugin, path string, body any) []contract.Run {
	t.Helper()
	st, b := api(t, p, "POST", path, body)
	var res contract.StartRunsResponse
	if err := json.Unmarshal(b, &res); st != 200 || err != nil {
		t.Fatalf("%s: %d %s", path, st, b)
	}
	return res.Runs
}

func getRun(t *testing.T, p *mindmapPlugin, id string) store.RunRecord {
	t.Helper()
	r, err := p.store.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func tool(t *testing.T, p *mindmapPlugin, taskID, name string, args map[string]any) *pluginsdk.AgentToolResult {
	t.Helper()
	res, err := p.InvokeAgentTool(context.Background(), &pluginsdk.AgentToolRequest{Name: name, Arguments: args,
		Context: pluginsdk.AgentToolContext{TaskID: taskID}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func state(p *mindmapPlugin, taskID, st string) {
	_ = p.OnEvent(context.Background(), event("task_session.state_changed", map[string]any{"task_id": taskID, "session_id": "sess-" + taskID, "new_state": st}))
}

func TestLaunchCreatesKandevTask(t *testing.T) {
	p, h := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	runs := apiRuns(t, p, "/api/maps/"+m.ID+"/runs", map[string]any{"node_id": m.RootID, "template": "research", "profile_id": "prof-claude",
		"goal": "Find three pricing examples.", "budget": map[string]any{"max_nodes": 12}})
	if len(runs) != 1 {
		t.Fatalf("runs: %+v", runs)
	}
	r := runs[0]
	if r.TaskID != "task-1" || r.Status != contract.RunQueued || r.Harness != contract.HarnessClaude || r.ProfileName != "Claude Researcher" ||
		r.Transport != contract.TransportMCP || r.Budget.MaxNodes != 12 {
		t.Fatalf("run: %+v", r)
	}
	if got, err := p.store.RunByTask("task-1"); err != nil || got.ID != r.ID {
		t.Fatalf("task_id -> run: %v %v", got.ID, err)
	}
	c := h.Created()
	if len(c) != 1 {
		t.Fatalf("created %d tasks", len(c))
	}
	in := c[0]
	if !in.StartAgent || in.Launch == nil || *in.Launch.AgentProfileID != "prof-claude" || in.WorkspaceID != "ws-1" ||
		in.Metadata["run_id"] != r.ID || in.Metadata["mindmap_id"] != m.ID || in.Metadata["node_id"] != m.RootID || len(in.Repositories) != 0 {
		t.Fatalf("create input: %+v", in)
	}
	if !strings.Contains(in.Title, "Product launch") || !strings.Contains(in.Description, "Mind map panel") {
		t.Fatalf("title/description: %q / %q", in.Title, in.Description)
	}
	// The node shows the run as claimed (queued) right away.
	if n, _ := p.store.Node(m.ID, m.RootID); n.Status != contract.StatusClaimed || n.RunID != r.ID {
		t.Fatalf("node ring: %s %s", n.Status, n.RunID)
	}
	// Unknown workspaces fall back to Kandev's default ("").
	m2 := newMapInWorkspace(t, p, "default")
	apiRuns(t, p, "/api/maps/"+m2.ID+"/runs", map[string]any{"node_id": m2.RootID, "template": "expand", "profile_id": "Cursor Fast"})
	if c := h.Created(); c[1].WorkspaceID != "" || *c[1].Launch.AgentProfileID != "prof-cursor" {
		t.Fatalf("default workspace / profile by name: %q %q", c[1].WorkspaceID, *c[1].Launch.AgentProfileID)
	}
}

func newMapInWorkspace(t *testing.T, p *mindmapPlugin, ws string) contract.Map {
	t.Helper()
	st, body := api(t, p, "POST", "/api/maps", map[string]any{"workspace_id": ws, "title": "Side project"})
	var m contract.Map
	if err := json.Unmarshal(body, &m); st != 200 || err != nil {
		t.Fatalf("create map: %d %s", st, body)
	}
	return m
}

func TestLaunchErrors(t *testing.T) {
	p, h := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	// When no profile is passed, the fallback default applies (pi).
	runs := apiRuns(t, p, "/api/maps/"+m.ID+"/runs", map[string]any{"node_id": m.RootID, "template": "research"})
	if runs[0].Harness != contract.HarnessPi {
		t.Fatalf("fallback default: %+v", runs[0])
	}

	// The plugin config default overrides the fallback.
	h.config["agent_profile"] = "prof-claude"
	runs = apiRuns(t, p, "/api/maps/"+m.ID+"/runs", map[string]any{"node_id": m.RootID, "template": "research"})
	if runs[0].Harness != contract.HarnessClaude {
		t.Fatalf("config default: %+v", runs[0])
	}
	st, b := api(t, p, "POST", "/api/maps/"+m.ID+"/runs", map[string]any{"node_id": m.RootID, "template": "research", "profile_id": "nope"})
	if st != 400 || !strings.Contains(string(b), "unknown agent profile") {
		t.Fatalf("unknown profile: %d %s", st, b)
	}
	// Execute needs an action node.
	st, b = api(t, p, "POST", "/api/maps/"+m.ID+"/runs", map[string]any{"node_id": m.RootID, "template": "execute", "profile_id": "prof-claude"})
	if st != 400 || !strings.Contains(string(b), "action") {
		t.Fatalf("execute on topic: %d %s", st, b)
	}
	// A failed launch returns the run as failed, with the reason.
	h.mu.Lock()
	h.createErr = errors.New("workflow has no start step")
	h.mu.Unlock()
	runs = apiRuns(t, p, "/api/maps/"+m.ID+"/runs", map[string]any{"node_id": m.RootID, "template": "research", "profile_id": "prof-claude"})
	if r := runs[0]; r.Status != contract.RunFailed || !strings.Contains(r.Error, "no start step") || r.TaskID != "" {
		t.Fatalf("failed launch: %+v", r)
	}
	if n, _ := p.store.Node(m.ID, m.RootID); n.Status != contract.StatusFailed {
		t.Fatalf("node after failed launch: %s", n.Status)
	}
}

func TestExecuteUsesRepo(t *testing.T) {
	p, h := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	host, _ := p.store.HostParticipant(m.ID)
	id := store.NewID(contract.PrefixNode)
	if _, err := p.svc.Submit(m.ID, []contract.Op{{ID: store.NewID(contract.PrefixOp), MapID: m.ID, Type: contract.OpNodeCreate,
		Payload: &contract.NodeCreatePayload{Node: contract.NodeInit{ID: id, ParentID: m.RootID, Order: "V", Title: "Add a pricing page", Kind: contract.KindAction}}}},
		app.SubmitOptions{Actor: contract.Actor{ParticipantID: host.ID}}); err != nil {
		t.Fatal(err)
	}
	runs := apiRuns(t, p, "/api/maps/"+m.ID+"/runs", map[string]any{"node_id": id, "template": "execute", "profile_id": "prof-cursor", "repo_id": "repo-1"})
	if runs[0].Template != contract.TemplateExecute {
		t.Fatalf("execute run: %+v", runs[0])
	}
	in := h.Created()[0]
	if len(in.Repositories) != 1 || in.Repositories[0].RepositoryID != "repo-1" || !strings.Contains(*in.Launch.Prompt, "Task: execute") {
		t.Fatalf("execute task: %+v", in.Repositories)
	}
	st, b := api(t, p, "GET", "/api/repos?map_id="+m.ID, nil)
	if st != 200 || !strings.Contains(string(b), "example-app") {
		t.Fatalf("repos: %d %s", st, b)
	}
}

func TestPromptPerHarness(t *testing.T) {
	p, h := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	start := func(profile, template string, extra map[string]any) (contract.Run, string) {
		body := map[string]any{"node_id": m.RootID, "template": template, "profile_id": profile}
		for k, v := range extra {
			body[k] = v
		}
		r := apiRuns(t, p, "/api/maps/"+m.ID+"/runs", body)[0]
		c := h.Created()
		return r, *c[len(c)-1].Launch.Prompt
	}

	r, claude := start("prof-claude", "research", nil)
	for _, want := range []string{"mcp__kandev__kandev_kandev_plugin_mindmap_<tool>", "select:mcp__kandev__kandev_kandev_plugin_mindmap_context,",
		"Task: research", "Call `context` first", m.RootID, "Mode: apply"} {
		if !strings.Contains(claude, want) {
			t.Errorf("claude prompt lacks %q", want)
		}
	}
	if strings.Contains(claude, "mmr1.") || strings.Contains(claude, "mindmap-patch") || r.Transport != contract.TransportMCP {
		t.Errorf("claude prompt carries the bridge token or patch fallback (transport %s)", r.Transport)
	}

	r, cursor := start("prof-cursor", "critique", nil)
	if !strings.Contains(cursor, "`kandev_kandev_plugin_mindmap_<tool>`") || strings.Contains(cursor, "select:") || r.Transport != contract.TransportMCP ||
		!strings.Contains(cursor, "Task: critique") {
		t.Errorf("cursor prompt:\n%s", cursor)
	}

	r, pi := start("prof-pi", "examples", map[string]any{"goal": "Find pricing pages."})
	for _, want := range []string{"mmr1.", "mindmap_context", "```mindmap-patch", "## Map outline", "[" + m.RootID + "]", "Task: find examples", "Goal: Find pricing pages."} {
		if !strings.Contains(pi, want) {
			t.Errorf("pi prompt lacks %q", want)
		}
	}
	if r.Transport != contract.TransportBridge || r.Harness != contract.HarnessPi {
		t.Errorf("pi run: %s %s", r.Transport, r.Harness)
	}
	if strings.Contains(pi, "{{RUN_TOKEN}}") {
		t.Error("token placeholder left in the prompt")
	}

	// Copilot is not Pi (whole-word harness match); unknown harnesses get the bridge.
	r, _ = start("prof-copilot", "research", nil)
	if r.Harness != contract.HarnessOther || r.Transport != contract.TransportBridge {
		t.Errorf("copilot run: %s %s", r.Harness, r.Transport)
	}

	// The gardener always proposes and says so; a lens adds its persona.
	r, garden := start("prof-claude", "garden", map[string]any{"mode": "apply"})
	if r.Mode != contract.ModePropose || !strings.Contains(garden, "Mode: propose") || !strings.Contains(garden, "Task: garden the map") {
		t.Errorf("garden: mode %s", r.Mode)
	}
	_, lens := start("prof-claude", "research", map[string]any{"lens": "futurist"})
	if !strings.Contains(lens, "Lens: Futurist") {
		t.Error("lens persona missing")
	}
	_, explore := start("prof-claude", "explore", map[string]any{"budget": map[string]any{"max_nodes": 30, "max_subagents": 2, "max_depth": 3}})
	if !strings.Contains(explore, "Task: explore autonomously") || !strings.Contains(explore, "up to 30 new nodes") || !strings.Contains(explore, "2 sub-agents") {
		t.Errorf("explore prompt:\n%s", explore)
	}
}

func TestProfilesEndpoint(t *testing.T) {
	p, _ := newTestPluginHost(t)
	st, b := api(t, p, "GET", "/api/profiles", nil)
	var ps []contract.AgentProfile
	if err := json.Unmarshal(b, &ps); st != 200 || err != nil || len(ps) != 4 {
		t.Fatalf("profiles: %d %s", st, b)
	}
	want := map[string]contract.Harness{"Claude Researcher": contract.HarnessClaude, "Cursor Fast": contract.HarnessCursor, "Pi Flash": contract.HarnessPi, "Copilot Default": contract.HarnessOther}
	for _, x := range ps {
		if want[x.Name] != x.Harness {
			t.Errorf("profile %q harness %s", x.Name, x.Harness)
		}
	}
}

func TestSwarm(t *testing.T) {
	p, h := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	runs := apiRuns(t, p, "/api/maps/"+m.ID+"/swarms", map[string]any{"node_id": m.RootID, "goal": "Stress-test the launch plan.",
		"budget": map[string]any{"max_nodes": 10},
		"agents": []any{
			map[string]any{"lens": "researcher", "profile_id": "prof-claude"},
			map[string]any{"lens": "skeptic", "profile_id": "prof-pi"},
			map[string]any{"lens": "examples_hunter", "profile_id": "prof-cursor"},
		}})
	if len(runs) != 4 {
		t.Fatalf("swarm runs: %d", len(runs))
	}
	coord := runs[0]
	if coord.TaskID != "" || coord.Lens != "" || coord.Template != contract.TemplateSwarm || coord.Status != contract.RunRunning ||
		coord.Budget.MaxNodes != 30 || coord.SwarmID == "" {
		t.Fatalf("coordinator: %+v", coord)
	}
	if len(h.Created()) != 3 {
		t.Fatalf("tasks created: %d", len(h.Created()))
	}
	snap, _ := p.store.Snapshot(m.ID)
	nodes := map[string]contract.Node{}
	for _, n := range snap.Nodes {
		nodes[n.ID] = n
	}
	harness := map[contract.Lens]contract.Harness{}
	branches := map[string]bool{}
	for _, r := range runs[1:] {
		harness[r.Lens] = r.Harness
		if r.ParentRunID != coord.ID || r.RootRunID != coord.ID || r.SwarmID != coord.SwarmID || r.TaskID == "" || r.Budget.MaxNodes != 10 {
			t.Fatalf("lens run: %+v", r)
		}
		n := nodes[r.NodeID]
		if n.ParentID != m.RootID || !strings.HasSuffix(n.Title, " lens") || branches[n.ID] {
			t.Fatalf("lens branch: %+v", n)
		}
		branches[n.ID] = true
		if n.Claim == nil || n.Claim.RunID != r.ID {
			t.Fatalf("lens branch not claimed by its agent: %+v", n.Claim)
		}
	}
	if harness[contract.LensResearcher] != contract.HarnessClaude || harness[contract.LensSkeptic] != contract.HarnessPi || harness[contract.LensExamplesHunter] != contract.HarnessCursor {
		t.Fatalf("mixed harnesses: %v", harness)
	}
	if !strings.Contains(*h.Created()[1].Launch.Prompt, "Lens: Skeptic") || !strings.Contains(*h.Created()[1].Launch.Prompt, "Task: swarm member") {
		t.Fatal("lens prompt")
	}
	// Each lens agent writes in its own branch; nodes count against the shared pool.
	lens := runs[1]
	res := tool(t, p, lens.TaskID, "add", map[string]any{"nodes": []any{map[string]any{"title": "Competitor pricing"}}})
	if res.IsError {
		t.Fatal(res.Text)
	}
	if left, _ := p.store.BudgetLeft(coord.ID); left.Nodes != 29 {
		t.Fatalf("pool left: %+v", left)
	}
	// The coordinator sums up its lens runs.
	for _, r := range runs[1:] {
		state(p, r.TaskID, "RUNNING")
	}
	if c := getRun(t, p, coord.ID); c.Status != contract.RunRunning {
		t.Fatalf("coordinator while running: %s", c.Status)
	}
	for _, r := range runs[1:] {
		tool(t, p, r.TaskID, "report", map[string]any{"summary": "Done.", "status": "done"})
		state(p, r.TaskID, "WAITING_FOR_INPUT")
	}
	if c := getRun(t, p, coord.ID); c.Status != contract.RunDone || c.Usage.Nodes != 1 || c.Usage.Subagents != 3 {
		t.Fatalf("coordinator after all done: %s %+v", c.Status, c.Usage)
	}
	if n, _ := p.store.Node(m.ID, m.RootID); n.Status != contract.StatusDone || n.RunID != coord.ID {
		t.Fatalf("swarm node ring: %s %s", n.Status, n.RunID)
	}
	// Bad lens.
	st, b := api(t, p, "POST", "/api/maps/"+m.ID+"/swarms", map[string]any{"node_id": m.RootID, "agents": []any{map[string]any{"lens": "poet"}}})
	if st != 400 {
		t.Fatalf("bad lens: %d %s", st, b)
	}
}

func TestSwarmStopStopsLenses(t *testing.T) {
	p, h := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	runs := apiRuns(t, p, "/api/maps/"+m.ID+"/swarms", map[string]any{"node_id": m.RootID,
		"agents": []any{map[string]any{"lens": "builder", "profile_id": "prof-claude"}, map[string]any{"lens": "futurist", "profile_id": "prof-claude"}}})
	if st, b := api(t, p, "POST", "/api/runs/"+runs[0].ID+"/stop", nil); st != 204 {
		t.Fatalf("stop swarm: %d %s", st, b)
	}
	for _, r := range runs {
		if got := getRun(t, p, r.ID); got.Status != contract.RunStopped {
			t.Fatalf("run %s after swarm stop: %s", r.ID, got.Status)
		}
	}
	if len(h.Sent()) != 2 {
		t.Fatalf("stop messages: %+v", h.Sent())
	}
}

func TestSpawnLaunchesWithinBudget(t *testing.T) {
	p, h := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	parent := apiRuns(t, p, "/api/maps/"+m.ID+"/runs", map[string]any{"node_id": m.RootID, "template": "explore", "profile_id": "prof-claude",
		"budget": map[string]any{"max_subagents": 1, "max_nodes": 20}})[0]
	res := tool(t, p, parent.TaskID, "add", map[string]any{"nodes": []any{map[string]any{"title": "Pricing", "ref": "pricing"}, map[string]any{"title": "Channels", "ref": "channels"}}})
	if res.IsError {
		t.Fatal(res.Text)
	}
	res = tool(t, p, parent.TaskID, "spawn", map[string]any{"agents": []any{
		map[string]any{"node_id": "@pricing", "goal": "Research pricing"}, map[string]any{"node_id": "@channels", "goal": "Research channels"}}})
	if !res.IsError || !strings.Contains(res.Text, "budget_exceeded") {
		t.Fatalf("over budget spawn: %s", res.Text)
	}
	res = tool(t, p, parent.TaskID, "spawn", map[string]any{"agents": []any{
		map[string]any{"node_id": "@pricing", "goal": "Research pricing", "template": "examples", "profile": "Pi Flash"}}})
	if res.IsError {
		t.Fatal(res.Text)
	}
	c := h.Created()
	if len(c) != 2 || c[1].Metadata["parent_run_id"] != parent.ID || *c[1].Launch.AgentProfileID != "prof-pi" || !strings.Contains(*c[1].Launch.Prompt, "Task: find examples") {
		t.Fatalf("spawned task: %+v", c[len(c)-1])
	}
	child, err := p.store.RunByTask("task-2")
	if err != nil || child.ParentRunID != parent.ID || child.RootRunID != parent.ID || child.Harness != contract.HarnessPi || child.Transport != contract.TransportBridge {
		t.Fatalf("child run: %+v %v", child.Run, err)
	}
	if child.Budget.MaxSubagents != 0 || child.Budget.MaxNodes != 18 {
		t.Fatalf("child budget clamped: %+v", child.Budget)
	}
	res = tool(t, p, parent.TaskID, "spawn", map[string]any{"agents": []any{map[string]any{"node_id": "@channels", "goal": "Research channels"}}})
	if !res.IsError || !strings.Contains(res.Text, "budget_exceeded") {
		t.Fatalf("second spawn: %s", res.Text)
	}
	// The child cannot spawn either: the pool is shared.
	res = tool(t, p, child.TaskID, "spawn", map[string]any{"agents": []any{map[string]any{"node_id": "@channels", "goal": "x"}}})
	if !res.IsError || !strings.Contains(res.Text, "budget_exceeded") {
		t.Fatalf("child spawn: %s", res.Text)
	}
}

func TestStopAndMessage(t *testing.T) {
	p, h := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	r := apiRuns(t, p, "/api/maps/"+m.ID+"/runs", map[string]any{"node_id": m.RootID, "template": "research", "profile_id": "prof-pi"})[0]
	tok := ""
	for _, line := range strings.Split(*h.Created()[0].Launch.Prompt, "\n") {
		if strings.HasPrefix(line, "mmr1.") {
			tok = line
		}
	}
	if tok == "" {
		t.Fatal("no run token in the pi prompt")
	}
	if _, err := p.store.LookupToken(tok); err != nil {
		t.Fatalf("token: %v", err)
	}
	state(p, r.TaskID, "RUNNING")
	tool(t, p, r.TaskID, "claim", map[string]any{"node_id": m.RootID})

	if st, b := api(t, p, "POST", "/api/runs/"+r.ID+"/message", map[string]any{"text": "Focus on B2B."}); st != 204 {
		t.Fatalf("message: %d %s", st, b)
	}
	if s := h.Sent(); len(s) != 1 || s[0].TaskID != r.TaskID || !strings.Contains(s[0].Text, "Focus on B2B.") {
		t.Fatalf("sent: %+v", s)
	}
	if st, _ := api(t, p, "POST", "/api/runs/"+r.ID+"/message", map[string]any{"text": "  "}); st != 400 {
		t.Fatalf("empty message: %d", st)
	}

	if st, b := api(t, p, "POST", "/api/runs/"+r.ID+"/stop", nil); st != 204 {
		t.Fatalf("stop: %d %s", st, b)
	}
	got := getRun(t, p, r.ID)
	if got.Status != contract.RunStopped || !got.StopRequested || got.EndedAt.IsZero() {
		t.Fatalf("after stop: %+v", got.Run)
	}
	if _, err := p.store.LookupToken(tok); err == nil {
		t.Fatal("run token still valid after stop")
	}
	if s := h.Sent(); len(s) != 2 || !strings.Contains(s[1].Text, "Stop now") {
		t.Fatalf("stop message: %+v", s)
	}
	if n, _ := p.store.Node(m.ID, m.RootID); n.Status != contract.StatusIdle || n.Claim != nil {
		t.Fatalf("node after stop: %s claim %+v", n.Status, n.Claim)
	}
	// Mid-turn tool calls from a stopped run cannot write; reads and report still work.
	if res := tool(t, p, r.TaskID, "add", map[string]any{"nodes": []any{map[string]any{"title": "Late idea"}}}); !res.IsError || !strings.Contains(res.Text, "run_ended") {
		t.Fatalf("write after stop: %s", res.Text)
	}
	if res := tool(t, p, r.TaskID, "report", map[string]any{"summary": "Stopped as asked.", "status": "review"}); res.IsError {
		t.Fatalf("report after stop: %s", res.Text)
	}
	// Late session states keep it stopped; stopping again is a no-op.
	state(p, r.TaskID, "RUNNING")
	state(p, r.TaskID, "WAITING_FOR_INPUT")
	if got := getRun(t, p, r.ID); got.Status != contract.RunStopped {
		t.Fatalf("late states: %s", got.Status)
	}
	if st, _ := api(t, p, "POST", "/api/runs/"+r.ID+"/stop", nil); st != 204 || len(h.Sent()) != 2 {
		t.Fatalf("second stop sent another message")
	}
	// A message does not revive a stopped run: no message, no fresh token.
	if st, b := api(t, p, "POST", "/api/runs/"+r.ID+"/message", map[string]any{"text": "Actually, continue."}); st != 409 {
		t.Fatalf("message after stop: %d %s", st, b)
	}
	if s := h.Sent(); len(s) != 2 {
		t.Fatalf("message to a stopped run was sent: %+v", s)
	}
	state(p, r.TaskID, "RUNNING")
	if got := getRun(t, p, r.ID); got.Status != contract.RunStopped || !got.StopRequested {
		t.Fatalf("stopped run revived: %s %v", got.Status, got.StopRequested)
	}
	if st, _ := api(t, p, "POST", "/api/runs/run_missing0000/stop", nil); st != 404 {
		t.Fatalf("missing run: %d", st)
	}
}

func TestAskHumanRoundTrip(t *testing.T) {
	p, h := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	r := apiRuns(t, p, "/api/maps/"+m.ID+"/runs", map[string]any{"node_id": m.RootID, "template": "research", "profile_id": "prof-claude"})[0]
	state(p, r.TaskID, "RUNNING")
	res := tool(t, p, r.TaskID, "ask_human", map[string]any{"question": "Which segment first?", "options": []any{"SMB", "Enterprise"}})
	if res.IsError {
		t.Fatal(res.Text)
	}
	qid, _ := res.StructuredContent["node_id"].(string)
	if got := getRun(t, p, r.ID); got.Status != contract.RunWaiting || got.WaitingReason != contract.WaitingQuestion {
		t.Fatalf("after ask: %s/%s", got.Status, got.WaitingReason)
	}
	// The turn ends while the question is open: still waiting on a human.
	state(p, r.TaskID, "WAITING_FOR_INPUT")
	if got := getRun(t, p, r.ID); got.WaitingReason != contract.WaitingQuestion {
		t.Fatalf("settle with open question: %s", got.WaitingReason)
	}
	if st, _ := api(t, p, "POST", "/api/maps/"+m.ID+"/nodes/"+m.RootID+"/answer", map[string]any{"answer": "x"}); st != 400 {
		t.Fatalf("answering a non-question: %d", st)
	}
	st, b := api(t, p, "POST", "/api/maps/"+m.ID+"/nodes/"+qid+"/answer", map[string]any{"answer": "SMB"})
	if st != 200 {
		t.Fatalf("answer: %d %s", st, b)
	}
	n, _ := p.store.Node(m.ID, qid)
	host, _ := p.store.HostParticipant(m.ID)
	if n.Question == nil || n.Question.Answer != "SMB" || n.Question.AnsweredBy != host.ID {
		t.Fatalf("answered node: %+v", n.Question)
	}
	waitFor(t, func() bool { return len(h.Sent()) == 1 })
	if s := h.Sent()[0]; s.TaskID != r.TaskID || !strings.Contains(s.Text, "SMB") || !strings.Contains(s.Text, qid) {
		t.Fatalf("relay: %+v", s)
	}
	waitFor(t, func() bool { return getRun(t, p, r.ID).Status == contract.RunRunning })
	// The agent sees the answer in context.
	if res := tool(t, p, r.TaskID, "context", map[string]any{}); !strings.Contains(res.Text, "answer: SMB") {
		t.Fatalf("context after answer: %s", res.Text)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestStatusTransitionsAndEarlyEvents(t *testing.T) {
	p, h := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	// The agent starts before Create returns: its RUNNING state must not be lost.
	h.onCreate = func(taskID string) { state(p, taskID, "RUNNING") }
	r := apiRuns(t, p, "/api/maps/"+m.ID+"/runs", map[string]any{"node_id": m.RootID, "template": "research", "profile_id": "prof-claude"})[0]
	h.onCreate = nil
	if got := getRun(t, p, r.ID); got.Status != contract.RunRunning || got.SessionID == "" {
		t.Fatalf("early RUNNING not replayed: %s", got.Status)
	}
	if n, _ := p.store.Node(m.ID, m.RootID); n.Status != contract.StatusRunning {
		t.Fatalf("node ring: %s", n.Status)
	}
	tool(t, p, r.TaskID, "report", map[string]any{"summary": "Added pricing research.", "status": "review"})
	state(p, r.TaskID, "WAITING_FOR_INPUT")
	if got := getRun(t, p, r.ID); got.Status != contract.RunReview {
		t.Fatalf("after report: %s", got.Status)
	}
	acts, _ := p.store.RecentActivity(m.ID, 50)
	var lines []string
	for _, a := range acts {
		lines = append(lines, a.Summary)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"started a research agent (Claude Researcher)", "run is running", "run is review"} {
		if !strings.Contains(joined, want) {
			t.Errorf("activity lacks %q:\n%s", want, joined)
		}
	}
	// A follow-up turn re-opens the run, and done settles it.
	state(p, r.TaskID, "RUNNING")
	if got := getRun(t, p, r.ID); got.Status != contract.RunRunning {
		t.Fatalf("re-opened: %s", got.Status)
	}
	state(p, r.TaskID, "COMPLETED")
	if got := getRun(t, p, r.ID); got.Status != contract.RunReview {
		t.Fatalf("completed after review report: %s", got.Status)
	}
}

func TestTimeBudgetStopsRun(t *testing.T) {
	p, h := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	r := apiRuns(t, p, "/api/maps/"+m.ID+"/runs", map[string]any{"node_id": m.RootID, "template": "explore", "profile_id": "prof-claude",
		"budget": map[string]any{"max_minutes": 5}})[0]
	idle := apiRuns(t, p, "/api/maps/"+m.ID+"/runs", map[string]any{"node_id": m.RootID, "template": "research", "profile_id": "prof-claude",
		"budget": map[string]any{"max_minutes": 5}})[0]
	state(p, idle.TaskID, "RUNNING")
	tool(t, p, idle.TaskID, "report", map[string]any{"summary": "Done.", "status": "review"})
	state(p, idle.TaskID, "WAITING_FOR_INPUT")
	state(p, r.TaskID, "RUNNING")
	p.svc.EnforceTimeBudgets(context.Background(), time.Now().UTC().Add(4*time.Minute))
	if got := getRun(t, p, r.ID); got.Status.Terminal() {
		t.Fatal("stopped too early")
	}
	p.svc.EnforceTimeBudgets(context.Background(), time.Now().UTC().Add(6*time.Minute))
	got := getRun(t, p, r.ID)
	if got.Status != contract.RunStopped || !strings.Contains(got.Error, "time budget") {
		t.Fatalf("after time budget: %s %q", got.Status, got.Error)
	}
	if got := getRun(t, p, idle.ID); got.Status != contract.RunReview {
		t.Fatalf("idle run in review was stopped: %s", got.Status)
	}
	if s := h.Sent(); len(s) != 1 || !strings.Contains(s[0].Text, "time budget") {
		t.Fatalf("stop message: %+v", s)
	}
}

func TestTaskPanelRoutes(t *testing.T) {
	p, _ := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	r := apiRuns(t, p, "/api/maps/"+m.ID+"/runs", map[string]any{"node_id": m.RootID, "template": "research", "profile_id": "prof-claude"})[0]
	tool(t, p, r.TaskID, "add", map[string]any{"nodes": []any{map[string]any{"title": "Pricing", "children": []any{map[string]any{"title": "Freemium"}}}}})
	st, b := api(t, p, "GET", "/api/tasks/"+r.TaskID+"/run", nil)
	var info contract.TaskRunInfo
	if err := json.Unmarshal(b, &info); st != 200 || err != nil {
		t.Fatalf("task run: %d %s", st, b)
	}
	if info.Run.ID != r.ID || info.MapTitle != "Product launch" || len(info.Path) != 1 || len(info.Branch) != 3 || info.Branch[2].Title != "Freemium" || info.Branch[2].Depth != 2 {
		t.Fatalf("task run info: %+v", info)
	}
	if st, _ := api(t, p, "GET", "/api/tasks/unknown-task/run", nil); st != 404 {
		t.Fatalf("unknown task: %d", st)
	}
	st, b = api(t, p, "GET", "/api/runs/tasks", nil)
	if st != 200 || !strings.Contains(string(b), r.TaskID) {
		t.Fatalf("run tasks: %d %s", st, b)
	}
}

func TestFailedRunKeepsAgentError(t *testing.T) {
	p, _ := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	r := apiRuns(t, p, "/api/maps/"+m.ID+"/runs", map[string]any{"node_id": m.RootID, "template": "research", "profile_id": "prof-claude"})[0]
	_ = p.OnEvent(context.Background(), event("task_session.state_changed", map[string]any{"task_id": r.TaskID, "new_state": "FAILED",
		"error_message": "", "session_metadata": map[string]any{"last_agent_error": map[string]any{"message": "Spend limit reached"}}}))
	if got := getRun(t, p, r.ID); got.Status != contract.RunFailed || got.Error != "Spend limit reached" {
		t.Fatalf("failed run: %s %q", got.Status, got.Error)
	}
}

func TestSwarmCapsApprovalAndKill(t *testing.T) {
	p, h := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	if st, b := api(t, p, "PUT", "/api/settings/share", map[string]any{"provider": "cloudflared", "max_concurrent_runs": 2, "require_guest_run_approval": true}); st != 200 {
		t.Fatalf("share settings: %d %s", st, b)
	}
	three := []any{map[string]any{"lens": "researcher", "profile_id": "prof-claude"}, map[string]any{"lens": "skeptic", "profile_id": "prof-claude"},
		map[string]any{"lens": "builder", "profile_id": "prof-claude"}}
	// The cap counts every lens agent up front: 3 > 2 starts none.
	st, b := api(t, p, "POST", "/api/maps/"+m.ID+"/swarms", map[string]any{"node_id": m.RootID, "agents": three})
	if st != 403 || !strings.Contains(string(b), string(contract.ErrBudgetExceeded)) || len(h.Created()) != 0 {
		t.Fatalf("swarm over cap: %d %s", st, b)
	}

	guest, err := p.store.CreateGuest(m.ID, "Bea", "")
	if err != nil {
		t.Fatal(err)
	}
	runs, err := p.svc.StartSwarm(context.Background(), app.SwarmSpec{MapID: m.ID, NodeID: m.RootID, StartedBy: guest.ID,
		Agents: []contract.SwarmMember{{Lens: contract.LensResearcher, ProfileID: "prof-claude"}, {Lens: contract.LensSkeptic, ProfileID: "prof-claude"}}})
	if err != nil || len(runs) != 3 || len(h.Created()) != 0 {
		t.Fatalf("guest swarm: %d runs, %d tasks, %v", len(runs), len(h.Created()), err)
	}
	for _, r := range runs[1:] {
		if r.Status != contract.RunWaiting || r.WaitingReason != contract.WaitingApproval || r.TaskID != "" {
			t.Fatalf("lens run not held: %+v", r)
		}
	}
	if c := getRun(t, p, runs[0].ID); c.Status != contract.RunWaiting || c.WaitingReason != contract.WaitingApproval {
		t.Fatalf("coordinator: %s %s", c.Status, c.WaitingReason)
	}
	if n, _ := p.store.CountActiveRuns(m.ID); n != 0 {
		t.Fatalf("held runs counted: %d", n)
	}
	approved := apiRuns(t, p, "/api/runs/"+runs[0].ID+"/approve", nil)
	if len(approved) != 2 || len(h.Created()) != 2 || approved[0].TaskID == "" || approved[1].TaskID == "" {
		t.Fatalf("approve: %+v", approved)
	}
	if st, b := api(t, p, "POST", "/api/runs/"+runs[0].ID+"/approve", nil); st != 409 {
		t.Fatalf("approve twice: %d %s", st, b)
	}

	if err := p.svc.StopAllRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sent := h.Sent(); len(sent) != 2 {
		t.Fatalf("kill messages: %+v", sent)
	}
	for _, r := range runs {
		if got := getRun(t, p, r.ID); got.Status != contract.RunStopped {
			t.Fatalf("run %s after kill: %s", r.ID, got.Status)
		}
	}
	if res := tool(t, p, approved[0].TaskID, "add", map[string]any{"nodes": []any{map[string]any{"title": "Late idea"}}}); !res.IsError {
		t.Fatal("stopped lens agent could still write")
	}
}
