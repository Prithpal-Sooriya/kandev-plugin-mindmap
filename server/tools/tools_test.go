package tools

import (
	"bytes"
	"context"
	"image"
	pngenc "image/png"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kandev-plugin-mindmap/server/app"
	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/hub"
	"kandev-plugin-mindmap/server/store"
)

type fixture struct {
	t     *testing.T
	st    *store.Store
	svc   *app.Service
	d     *Dispatcher
	impl  *Tools
	m     contract.Map
	host  contract.Participant
	run   store.RunRecord
	token string
}

func setup(t *testing.T, mode contract.RunMode) *fixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := hub.New()
	t.Cleanup(func() { h.Close(); _ = st.Close() })
	svc := app.New(st, h)
	svc.Launcher = &app.FakeLauncher{}
	svc.SetPort(45678)
	d, impl := NewDispatcher(svc)
	m, host, err := st.CreateMap(contract.CreateMapRequest{WorkspaceID: "ws-1", Title: "Pricing"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.StartRun(context.Background(), store.NewRun{MapID: m.ID, NodeID: m.RootID, ProfileID: "prof-1", Mode: mode,
		Budget: &contract.Budget{MaxNodes: 10, MaxSubagents: 2, MaxDepth: 3}, StartedBy: host.ID})
	if err != nil {
		t.Fatal(err)
	}
	fl := svc.Launcher.(*app.FakeLauncher)
	return &fixture{t: t, st: st, svc: svc, d: d, impl: impl, m: m, host: host, run: run, token: fl.Launches[0].RunToken}
}

func (f *fixture) mcp(name string, args string) contract.ToolResult {
	return f.d.Invoke(context.Background(), Caller{Transport: contract.TransportMCP, TaskID: f.run.TaskID}, name, []byte(args))
}

func (f *fixture) ok(name, args string) contract.ToolResult {
	f.t.Helper()
	r := f.mcp(name, args)
	if r.IsError {
		f.t.Fatalf("%s %s: %s", name, args, r.Text)
	}
	return r
}

func errCode(r contract.ToolResult) string {
	if !r.IsError {
		return ""
	}
	e, _ := r.Structured["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func TestAccessChecks(t *testing.T) {
	f := setup(t, contract.ModeApply)
	ctx := context.Background()
	// A task that is not a map run sees nothing, even read-only tools.
	r := f.d.Invoke(ctx, Caller{Transport: contract.TransportMCP, TaskID: "task-unrelated"}, "context", []byte(`{}`))
	if errCode(r) != "not_a_run" {
		t.Fatalf("unrelated task: %s", r.Text)
	}
	r = f.d.Invoke(ctx, Caller{Transport: contract.TransportBridge, Token: "mmr1.45678.bogusbogusbogusbogusbogusbogusbogusbogusbog"}, "read", []byte(`{}`))
	if errCode(r) != "unauthorized" {
		t.Fatalf("bad bridge token: %s", r.Text)
	}
	// Missing required args are clear bad_requests (agents with deferred schemas send {}).
	for name, field := range map[string]string{"add": "nodes", "update": "node_id", "link": "from_id", "report": "summary", "ask_human": "question", "spawn": "agents"} {
		r := f.mcp(name, `{}`)
		if errCode(r) != "bad_request" || !strings.Contains(r.Text, field) {
			t.Errorf("%s {}: %s", name, r.Text)
		}
	}
	// Bridge token and MCP task resolve to the same run.
	r = f.d.Invoke(ctx, Caller{Transport: contract.TransportBridge, Token: f.token}, "context", []byte(`{}`))
	if r.IsError || !strings.Contains(r.Text, f.run.ID) {
		t.Fatalf("bridge context: %s", r.Text)
	}
	// Reads across the workspace are allowed; writes to another map are not.
	other, _, _ := f.st.CreateMap(contract.CreateMapRequest{WorkspaceID: "ws-1", Title: "Other map"})
	if r := f.mcp("read", `{"map_id":"`+other.ID+`"}`); r.IsError || !strings.Contains(r.Text, "Other map") {
		t.Fatalf("cross-map read: %s", r.Text)
	}
	if r := f.mcp("add", `{"parent_id":"`+other.RootID+`","nodes":[{"title":"x"}]}`); errCode(r) != "forbidden" {
		t.Fatalf("cross-map write: %s", r.Text)
	}
	foreign, _, _ := f.st.CreateMap(contract.CreateMapRequest{WorkspaceID: "ws-2", Title: "Foreign"})
	if r := f.mcp("read", `{"map_id":"`+foreign.ID+`"}`); errCode(r) != "forbidden" {
		t.Fatalf("cross-workspace read: %s", r.Text)
	}
	// Terminal runs keep working through the grace period, then run_ended.
	_, _ = f.svc.UpdateRun(f.run.ID, func(r *store.RunRecord) bool { r.Status = contract.RunDone; return true })
	if r := f.mcp("context", `{}`); r.IsError {
		t.Fatalf("within grace: %s", r.Text)
	}
	f.impl.now = func() time.Time { return time.Now().Add(contract.RunTokenGrace + time.Minute) }
	if r := f.mcp("context", `{}`); errCode(r) != "run_ended" {
		t.Fatalf("after grace: %s", r.Text)
	}
}

func TestAddReadSearchUpdateLink(t *testing.T) {
	f := setup(t, contract.ModeApply)
	r := f.ok("add", `{"nodes":[{"ref":"tiers","title":"Three-tier freemium","kind":"idea","confidence":0.6,"children":[
		{"title":"Example Co free tier","kind":"example","sources":[{"url":"https://example.com/pricing","quote":"Free for 3."}]}]},
		{"ref":"usage","title":"Usage-based pricing"}]}`)
	var out contract.WriteOutcome
	b, _ := json.Marshal(r.Structured)
	_ = json.Unmarshal(b, &out)
	if !out.Applied || len(out.NodeIDs) != 3 || out.Refs["tiers"] == "" || out.Budget.Nodes != 7 {
		t.Fatalf("outcome %+v", out)
	}
	read := f.ok("read", `{"include_bodies":true}`)
	if !strings.Contains(read.Text, "idea · conf 0.6 · Three-tier freemium") || !strings.Contains(read.Text, "example · 1 src · Example Co free tier") {
		t.Fatalf("outline:\n%s", read.Text)
	}
	if s := f.ok("search", `{"query":"freemium"}`); !strings.Contains(s.Text, out.Refs["tiers"]) {
		t.Fatalf("search: %s", s.Text)
	}
	if s := f.ok("search", `{"kinds":["example"]}`); !strings.Contains(s.Text, "Example Co") || strings.Contains(s.Text, "Usage-based") {
		t.Fatalf("kind filter: %s", s.Text)
	}
	f.ok("update", `{"node_id":"@tiers","append_body":"Tier limits matter.","add_tags":["pricing"],"add_sources":[{"url":"https://example.com/b"}]}`)
	n, _ := f.st.Node(f.m.ID, out.Refs["tiers"])
	if n.BodyMD != "Tier limits matter." || len(n.Tags) != 1 || len(n.Sources) != 1 || n.Author.RunID != f.run.ID {
		t.Fatalf("node %+v", n)
	}
	if r := f.mcp("link", `{"from_id":"@tiers","to_id":"@usage","label":"contrasts"}`); r.IsError {
		t.Fatal(r.Text)
	}
	if r := f.mcp("update", `{"node_id":"@nope","title":"x"}`); errCode(r) != "not_found" {
		t.Fatalf("unknown ref: %s", r.Text)
	}
	// Budget: 7 left, adding 8 fails and writes nothing.
	many := `{"nodes":[` + strings.TrimSuffix(strings.Repeat(`{"title":"n"},`, 8), ",") + `]}`
	if r := f.mcp("add", many); errCode(r) != "budget_exceeded" {
		t.Fatalf("budget: %s", r.Text)
	}
	// Depth: max 3 below the run node.
	if r := f.mcp("add", `{"parent_id":"@tiers","nodes":[{"title":"a","children":[{"title":"b","children":[{"title":"c"}]}]}]}`); errCode(r) != "budget_exceeded" {
		t.Fatalf("depth: %s", r.Text)
	}
}

func TestRestructureIsAtomicAndRetireSoftDeletes(t *testing.T) {
	f := setup(t, contract.ModeApply)
	f.ok("add", `{"nodes":[{"ref":"a","title":"A","children":[{"ref":"a1","title":"A1"}]},{"ref":"b","title":"B"},{"ref":"c","title":"C"}]}`)
	before, _ := f.st.GetMap(f.m.ID)
	// Second op is a cycle: nothing applies.
	r := f.mcp("restructure", `{"reason":"tidy","ops":[{"op":"rename","node_id":"@b","title":"B2"},{"op":"move","node_id":"@a","parent_id":"@a1"}]}`)
	if errCode(r) != "cycle" {
		t.Fatalf("want cycle: %s", r.Text)
	}
	if after, _ := f.st.GetMap(f.m.ID); after.Rev != before.Rev {
		t.Fatal("partial restructure applied")
	}
	f.ok("restructure", `{"reason":"group the options","ops":[{"op":"regroup","node_ids":["@b","@c"],"title":"Options","ref":"opts"},
		{"op":"move","node_id":"@a","parent_id":"@opts"},{"op":"retire","node_id":"@a1"}]}`)
	snap, _ := f.st.Snapshot(f.m.ID)
	if err := contract.ValidateTree(snap.Nodes); err != nil {
		t.Fatal(err)
	}
	read := f.ok("read", `{"node_id":"@opts"}`)
	for _, want := range []string{"Options", "· B", "· C", "· A"} {
		if !strings.Contains(read.Text, want) {
			t.Fatalf("missing %q in\n%s", want, read.Text)
		}
	}
	if strings.Contains(read.Text, "A1") {
		t.Fatalf("retired node still visible:\n%s", read.Text)
	}
}

func TestProposeModeQueuesAndReviewApplies(t *testing.T) {
	f := setup(t, contract.ModePropose)
	r := f.ok("add", `{"nodes":[{"ref":"g","title":"Ghost idea"}]}`)
	if r.Structured["applied"] != false || r.Structured["proposal_id"] != f.run.ProposalID {
		t.Fatalf("propose add: %+v", r.Structured)
	}
	// Later calls can reference the ghost.
	f.ok("add", `{"parent_id":"@g","nodes":[{"title":"Ghost child"}]}`)
	if snap, _ := f.st.Snapshot(f.m.ID); len(snap.Nodes) != 1 {
		t.Fatalf("propose applied nodes: %d", len(snap.Nodes))
	}
	// Claims and questions stay live in propose mode.
	f.ok("claim", `{"node_id":"`+f.m.RootID+`"}`)
	f.ok("report", `{"summary":"Two ideas.","status":"review"}`)
	p, _ := f.st.GetProposal(f.run.ProposalID)
	if p.Status != contract.ProposalReady || len(p.Items) != 2 {
		t.Fatalf("proposal %+v", p)
	}
	if _, err := f.svc.Review(p.ID, contract.ReviewProposalRequest{AcceptAll: true}, f.host.ID); err != nil {
		t.Fatal(err)
	}
	if snap, _ := f.st.Snapshot(f.m.ID); len(snap.Nodes) != 3 {
		t.Fatalf("accepted nodes: %d", len(snap.Nodes))
	}
}

func TestClaimSpawnAskHumanWriteDocReport(t *testing.T) {
	f := setup(t, contract.ModeApply)
	f.ok("add", `{"nodes":[{"ref":"x","title":"Frontier"}]}`)
	f.ok("claim", `{"node_id":"@x","note":"researching"}`)
	// Another run cannot claim it.
	other, err := f.svc.StartRun(context.Background(), store.NewRun{MapID: f.m.ID, NodeID: f.m.RootID, StartedBy: f.host.ID})
	if err != nil {
		t.Fatal(err)
	}
	xid, _ := f.st.ResolveRef(f.run.RootRunID, "x")
	r := f.d.Invoke(context.Background(), Caller{Transport: contract.TransportMCP, TaskID: other.TaskID}, "claim", []byte(`{"node_id":"`+xid+`"}`))
	if errCode(r) != "claimed" {
		t.Fatalf("double claim: %s", r.Text)
	}
	// Spawn charges the sub-agent pool (2) and fails beyond it.
	sp := f.ok("spawn", `{"agents":[{"node_id":"@x","goal":"Find examples","lens":"skeptic"}]}`)
	runs := sp.Structured["runs"].([]any)
	child := runs[0].(map[string]any)
	if child["task_id"] == "" {
		t.Fatalf("spawned run has no task: %+v", child)
	}
	cr, _ := f.st.GetRun(child["run_id"].(string))
	if cr.ParentRunID != f.run.ID || cr.RootRunID != f.run.ID || cr.Budget.MaxSubagents != 1 {
		t.Fatalf("child run %+v", cr.Run)
	}
	if r := f.mcp("spawn", `{"agents":[{"node_id":"@x","goal":"a"},{"node_id":"@x","goal":"b"}]}`); errCode(r) != "budget_exceeded" {
		t.Fatalf("spawn over budget: %s", r.Text)
	}
	// Child refs resolve in the shared run tree.
	r = f.d.Invoke(context.Background(), Caller{Transport: contract.TransportMCP, TaskID: cr.TaskID}, "add", []byte(`{"parent_id":"@x","nodes":[{"title":"From child"}]}`))
	if r.IsError {
		t.Fatalf("child add via parent ref: %s", r.Text)
	}
	// ask_human creates a live question and parks the run.
	q := f.ok("ask_human", `{"question":"Which segment first?","options":["SMB","Enterprise"]}`)
	qid := q.Structured["node_id"].(string)
	qn, _ := f.st.Node(f.m.ID, qid)
	if qn.Kind != contract.KindQuestion || qn.Question == nil || qn.Question.RunID != f.run.ID {
		t.Fatalf("question node %+v", qn)
	}
	if rr, _ := f.st.GetRun(f.run.ID); rr.Status != contract.RunWaiting || rr.WaitingReason != contract.WaitingQuestion {
		t.Fatalf("run after ask_human %s/%s", rr.Status, rr.WaitingReason)
	}
	// write_doc creates and then updates a doc node.
	wd := f.ok("write_doc", `{"title":"Pricing","path":"pricing/index.md","body":"# Pricing","ref":"doc","source_ids":["@x"]}`)
	did := wd.Structured["node_id"].(string)
	f.ok("write_doc", `{"doc_id":"@doc","title":"Pricing v2","path":"pricing/index.md","body":"# Pricing v2"}`)
	dn, _ := f.st.Node(f.m.ID, did)
	if dn.Kind != contract.KindDoc || dn.Title != "Pricing v2" || dn.Doc == nil || len(dn.Doc.SourceIDs) != 1 {
		t.Fatalf("doc %+v", dn)
	}
	if r := f.mcp("add", `{"nodes":[{"title":"d","kind":"doc"}]}`); errCode(r) != "bad_request" {
		t.Fatalf("add kind doc: %s", r.Text)
	}
	f.ok("report", `{"summary":"Done exploring.","status":"done","next_steps":["Price it"]}`)
	rr, _ := f.st.GetRun(f.run.ID)
	if !rr.Reported || rr.ReportStatus != contract.ReportDone || !strings.Contains(rr.Summary, "Price it") {
		t.Fatalf("report %+v", rr.Run)
	}
	root, _ := f.st.Node(f.m.ID, f.m.RootID)
	if !strings.Contains(root.BodyMD, "Done exploring.") {
		t.Fatalf("summary not on node: %q", root.BodyMD)
	}
}

func TestAttachSources(t *testing.T) {
	f := setup(t, contract.ModeApply)
	var buf bytes.Buffer
	_ = pngenc.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 3)))
	png := buf.Bytes()
	b64 := base64.StdEncoding.EncodeToString(png)
	r := f.ok("attach", `{"node_id":"`+f.m.RootID+`","base64":"`+b64+`","mime_type":"image/png","caption":"Shot"}`)
	root, _ := f.st.Node(f.m.ID, f.m.RootID)
	if len(root.Assets) != 1 || root.Assets[0].Width != 2 || root.Assets[0].Height != 3 || root.Assets[0].Caption != "Shot" {
		t.Fatalf("assets %+v (%s)", root.Assets, r.Text)
	}
	// A declared image that is not one is refused.
	if r := f.mcp("attach", `{"node_id":"`+f.m.RootID+`","base64":"aGVsbG8=","mime_type":"image/png"}`); errCode(r) != "unsupported_media" {
		t.Fatalf("fake png: %s", r.Text)
	}
	// file_path: only inside the allowed roots.
	dir := t.TempDir()
	f.impl.FileRoots = []string{dir}
	good := filepath.Join(dir, "notes.md")
	_ = os.WriteFile(good, []byte("# notes"), 0o644)
	if r := f.mcp("attach", `{"node_id":"`+f.m.RootID+`","file_path":"`+good+`"}`); r.IsError {
		t.Fatalf("file in root: %s", r.Text)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	_ = os.WriteFile(outside, []byte("secret"), 0o644)
	if r := f.mcp("attach", `{"node_id":"`+f.m.RootID+`","file_path":"`+outside+`"}`); errCode(r) != "forbidden" {
		t.Fatalf("file outside roots: %s", r.Text)
	}
	if r := f.mcp("attach", `{"node_id":"`+f.m.RootID+`","file_path":"relative.png"}`); errCode(r) != "bad_request" {
		t.Fatalf("relative path: %s", r.Text)
	}
	// url: fetched with type and size checks; loopback refused unless allowed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/big.png" {
			w.Header().Set("Content-Length", "20000000")
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer srv.Close()
	if r := f.mcp("attach", `{"node_id":"`+f.m.RootID+`","url":"`+srv.URL+`/a.png"}`); errCode(r) != "fetch_failed" {
		t.Fatalf("loopback url: %s", r.Text)
	}
	f.impl.AllowPrivateURLs = true
	if r := f.mcp("attach", `{"node_id":"`+f.m.RootID+`","url":"`+srv.URL+`/a.png"}`); r.IsError {
		t.Fatalf("url attach: %s", r.Text)
	}
	root, _ = f.st.Node(f.m.ID, f.m.RootID)
	if len(root.Assets) != 3 || root.Assets[2].SourceURL != srv.URL+"/a.png" {
		t.Fatalf("assets after url %+v", root.Assets)
	}
}

func TestPersonalTokenActsAsHost(t *testing.T) {
	f := setup(t, contract.ModeApply)
	raw, _, err := f.st.IssueToken(contract.TokenPersonal, 45678, "", "", "", "", "laptop", 0)
	if err != nil {
		t.Fatal(err)
	}
	c := Caller{Transport: contract.TransportBridge, Token: raw}
	if r := f.d.Invoke(context.Background(), c, "add", []byte(`{"nodes":[{"title":"x"}]}`)); errCode(r) != "bad_request" {
		t.Fatalf("personal add without parent: %s", r.Text)
	}
	r := f.d.Invoke(context.Background(), c, "add", []byte(`{"parent_id":"`+f.m.RootID+`","nodes":[{"title":"From laptop"}]}`))
	if r.IsError {
		t.Fatal(r.Text)
	}
	id := r.Structured["node_ids"].([]any)[0].(string)
	n, _ := f.st.Node(f.m.ID, id)
	if n.Author.ParticipantID != f.host.ID {
		t.Fatalf("personal write author %+v", n.Author)
	}
	if r := f.d.Invoke(context.Background(), c, "context", []byte(`{}`)); r.IsError || !strings.Contains(r.Text, f.m.ID) {
		t.Fatalf("personal context: %s", r.Text)
	}
}
