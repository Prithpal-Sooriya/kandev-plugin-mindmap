package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"kandev-plugin-mindmap/server/app"
	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/store"
	"kandev-plugin-mindmap/server/web"
)

// addKids creates child nodes under parent as the host and returns their ids.
func addKids(t *testing.T, p *mindmapPlugin, m contract.Map, parent string, body string, titles ...string) []string {
	t.Helper()
	host, err := p.store.HostParticipant(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(titles))
	ops := make([]contract.Op, len(titles))
	last := ""
	for i, title := range titles {
		ids[i] = store.NewID(contract.PrefixNode)
		last = contract.OrderBetween(last, "")
		ops[i] = contract.Op{ID: store.NewID(contract.PrefixOp), MapID: m.ID, Type: contract.OpNodeCreate, Payload: &contract.NodeCreatePayload{
			Node: contract.NodeInit{ID: ids[i], ParentID: parent, Order: last, Title: title, BodyMD: body, Kind: contract.KindIdea}}}
	}
	if _, err := p.svc.Submit(m.ID, ops, app.SubmitOptions{Actor: contract.Actor{ParticipantID: host.ID}}); err != nil {
		t.Fatal(err)
	}
	return ids
}

// compileMap is a synthetic "Note-taking app" map: Pricing has 9 children
// (splits), Research has a long body (splits), Risks is small.
func compileMap(t *testing.T, p *mindmapPlugin) (contract.Map, []string) {
	t.Helper()
	m := newMapViaAPI(t, p)
	branches := addKids(t, p, m, m.RootID, "", "Pricing", "Research", "Risks")
	addKids(t, p, m, branches[0], "", "Free tier", "Pro tier", "Team tier", "Student discount", "Annual plan", "Trials", "Refunds", "Taxes", "Currency")
	addKids(t, p, m, branches[1], strings.Repeat("word ", 1600), "Interviews")
	addKids(t, p, m, branches[2], "", "Sync conflicts")
	return m, branches
}

func docNodes(t *testing.T, p *mindmapPlugin, m contract.Map) map[string]contract.Node {
	t.Helper()
	snap, err := p.store.Snapshot(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]contract.Node{}
	for _, n := range snap.Nodes {
		if n.Kind == contract.KindDoc {
			out[n.ID] = n
		}
	}
	return out
}

func prompt(h *fakeHost, i int) string { return *h.Created()[i].Launch.Prompt }

func TestCompilePlansSectionsThenSynthesis(t *testing.T) {
	p, h := newTestPluginHost(t)
	exportDir := t.TempDir()
	h.config["export_dir"] = exportDir
	m, branches := compileMap(t, p)
	runs := apiRuns(t, p, "/api/maps/"+m.ID+"/compile", map[string]any{"node_id": m.RootID, "profile_id": "prof-claude", "slug": "Note App"})
	if len(runs) != 4 {
		t.Fatalf("compile runs: %d", len(runs))
	}
	coord := runs[0]
	if coord.Template != contract.TemplateCompile || coord.TaskID != "" || coord.ParentRunID != "" || coord.Status != contract.RunRunning || coord.Budget.MaxSubagents != 4 {
		t.Fatalf("coordinator: %+v", coord)
	}
	if n, _ := p.store.CountActiveRuns(m.ID); n != 3 {
		t.Fatalf("active runs (coordinator must not count): %d", n)
	}
	// One section run per top-level branch, in parallel, as children of the coordinator.
	if len(h.Created()) != 3 {
		t.Fatalf("tasks: %d", len(h.Created()))
	}
	for i, r := range runs[1:] {
		if r.Template != contract.TemplateCompile || r.ParentRunID != coord.ID || r.NodeID != branches[i] || r.TaskID == "" || r.Budget.MaxNodes != 20 {
			t.Fatalf("section run %d: %+v", i, r)
		}
	}
	// The doc tree exists up front: index, single doc, one doc per section.
	docs := docNodes(t, p, m)
	var root, single contract.Node
	paths := map[string]contract.Node{}
	for _, d := range docs {
		paths[d.Doc.Path] = d
		if d.ParentID == m.RootID {
			root = d
		}
	}
	single = paths["note-app.md"]
	if len(docs) != 5 || root.Doc.Path != "index.md" || single.ParentID != root.ID {
		t.Fatalf("doc placeholders: %+v", paths)
	}
	for _, dir := range []string{"pricing", "research", "risks"} {
		if d, ok := paths[dir+"/index.md"]; !ok || d.ParentID != root.ID {
			t.Fatalf("section doc %s missing: %+v", dir, paths)
		}
	}
	pricingDoc := paths["pricing/index.md"]
	// Section prompts: the compile template, their doc id, and the split rule.
	pr := prompt(h, 0)
	if !strings.Contains(pr, "Task: compile a section") || !strings.Contains(pr, `doc_id: "`+pricingDoc.ID+`"`) ||
		!strings.Contains(pr, "so split it") || !strings.Contains(pr, "pricing/student-discount.md") || !strings.Contains(pr, "`../risks/index.md`") {
		t.Fatalf("pricing prompt:\n%s", pr)
	}
	if !strings.Contains(prompt(h, 1), "so split it") {
		t.Fatal("research (long) should split")
	}
	if rp := prompt(h, 2); strings.Contains(rp, "so split it") || !strings.Contains(rp, "Write one doc for the whole branch") {
		t.Fatalf("risks should not split:\n%s", rp)
	}

	// Section agents write their docs and finish; the synthesis waits for all of them.
	write := func(taskID string, args map[string]any) {
		t.Helper()
		if res := tool(t, p, taskID, "write_doc", args); res.IsError {
			t.Fatalf("write_doc: %s", res.Text)
		}
	}
	write(runs[1].TaskID, map[string]any{"doc_id": pricingDoc.ID, "title": "Pricing", "path": "pricing/index.md",
		"body": "# Pricing\n\nSee [Student discount](student-discount.md).", "source_ids": []string{branches[0]}})
	write(runs[1].TaskID, map[string]any{"parent_doc_id": pricingDoc.ID, "title": "Student discount", "path": "pricing/student-discount.md",
		"body": "Half price with a school email."})
	for i, r := range runs[1:] {
		tool(t, p, r.TaskID, "report", map[string]any{"summary": "Wrote the section.", "status": "done"})
		state(p, r.TaskID, "WAITING_FOR_INPUT")
		if i < 2 && len(h.Created()) != 3 {
			t.Fatal("synthesis started before every section settled")
		}
	}
	if len(h.Created()) != 4 {
		t.Fatalf("synthesis not started: %d tasks", len(h.Created()))
	}
	synth, err := p.store.RunByTask("task-4")
	if err != nil || synth.Template != contract.TemplateSynthesis || synth.ParentRunID != coord.ID || synth.NodeID != m.RootID {
		t.Fatalf("synthesis run: %+v %v", synth, err)
	}
	sp := prompt(h, 3)
	if !strings.Contains(sp, "Task: synthesise") || !strings.Contains(sp, `doc_id: "`+single.ID+`"`) || !strings.Contains(sp, `doc_id: "`+root.ID+`"`) ||
		!strings.Contains(sp, "plugin writes the doc nodes to the export folder") {
		t.Fatalf("synthesis prompt:\n%s", sp)
	}
	if c := getRun(t, p, coord.ID); c.Status != contract.RunRunning {
		t.Fatalf("coordinator during synthesis: %s", c.Status)
	}
	write(synth.TaskID, map[string]any{"doc_id": root.ID, "title": "Overview", "path": "index.md", "body": "# Overview\n\n- [Pricing](pricing/index.md)"})
	write(synth.TaskID, map[string]any{"doc_id": single.ID, "title": "Note app", "path": "note-app.md", "body": "# Note app\n\nEverything in one place."})
	tool(t, p, synth.TaskID, "report", map[string]any{"summary": "Wrote the overview.", "status": "done"})
	state(p, synth.TaskID, "WAITING_FOR_INPUT")
	if c := getRun(t, p, coord.ID); c.Status != contract.RunDone {
		t.Fatalf("coordinator after synthesis: %s (%s)", c.Status, c.Error)
	}

	// No repository: the backend wrote the files to export_dir/<map id>.
	mapDir := filepath.Join(exportDir, m.ID)
	for _, f := range []string{"note-app.md", "note-app/index.md", "note-app/pricing/index.md", "note-app/pricing/student-discount.md",
		"note-app/research/index.md", "note-app/risks/index.md"} {
		if _, err := os.Stat(filepath.Join(mapDir, f)); err != nil {
			t.Errorf("exported file %s: %v", f, err)
		}
	}
	if fi, err := os.Stat(filepath.Join(mapDir, "note-app", "assets")); err != nil || !fi.IsDir() {
		t.Errorf("assets folder: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(mapDir, "note-app.md")); !strings.Contains(string(b), "Everything in one place.") {
		t.Errorf("single doc content: %q", b)
	}
	st, b := api(t, p, "GET", "/api/maps/"+m.ID+"/docs", nil)
	var trees []contract.DocTree
	if err := json.Unmarshal(b, &trees); st != 200 || err != nil || len(trees) != 1 {
		t.Fatalf("docs: %d %s", st, b)
	}
	tr := trees[0]
	if tr.Slug != "note-app" || tr.ExportedTo != filepath.Join(mapDir, "note-app") || tr.Single == nil || tr.Single.NodeID != single.ID ||
		tr.Index.NodeID != root.ID || len(tr.Index.Children) != 3 || len(tr.Index.Children[0].Children) != 1 || tr.RootNodeID != m.RootID {
		t.Fatalf("doc tree: %+v", tr)
	}
}

func TestExportZipLayout(t *testing.T) {
	p, h := newTestPluginHost(t)
	h.config["export_dir"] = t.TempDir()
	m, _ := compileMap(t, p)
	if st, b := api(t, p, "GET", "/api/maps/"+m.ID+"/export.zip", nil); st != 404 {
		t.Fatalf("zip before any compile: %d %s", st, b)
	}
	runs := apiRuns(t, p, "/api/maps/"+m.ID+"/compile", map[string]any{"node_id": m.RootID, "profile_id": "prof-claude"})
	var pricing contract.Node
	for _, d := range docNodes(t, p, m) {
		if d.Doc.Path == "pricing/index.md" {
			pricing = d
		}
	}
	if res := tool(t, p, runs[1].TaskID, "write_doc", map[string]any{"parent_doc_id": pricing.ID, "title": "Tiers", "path": "pricing/tiers.md", "body": "Three tiers."}); res.IsError {
		t.Fatal(res.Text)
	}
	// A doc with a bad or duplicate path still gets a place.
	if res := tool(t, p, runs[1].TaskID, "write_doc", map[string]any{"parent_doc_id": pricing.ID, "title": "Tiers again", "path": "pricing/tiers.md", "body": "Dup."}); res.IsError {
		t.Fatal(res.Text)
	}
	// The zip is built from the doc nodes: it works before the agents finish.
	st, b := api(t, p, "GET", "/api/maps/"+m.ID+"/export.zip?node_id="+pricing.ID, nil)
	if st != 200 {
		t.Fatalf("zip: %d %s", st, b)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	want := []string{"product-launch.md", "product-launch/assets/", "product-launch/index.md", "product-launch/pricing/index.md",
		"product-launch/pricing/tiers-again.md", "product-launch/pricing/tiers.md", "product-launch/research/index.md", "product-launch/risks/index.md"}
	if !slices.Equal(names, want) {
		t.Fatalf("zip layout:\n got %v\nwant %v", names, want)
	}
	for _, f := range zr.File {
		if f.Name == "product-launch/pricing/tiers.md" {
			rc, _ := f.Open()
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(rc)
			if buf.String() != "# Tiers\n\nThree tiers.\n" {
				t.Fatalf("tiers.md: %q", buf.String())
			}
		}
	}
	// POST export writes to disk on demand (host only).
	st, b = api(t, p, "POST", "/api/maps/"+m.ID+"/export", nil)
	var tree contract.DocTree
	if err := json.Unmarshal(b, &tree); st != 200 || err != nil || tree.ExportedTo == "" {
		t.Fatalf("export to disk: %d %s", st, b)
	}
	if _, err := os.Stat(filepath.Join(tree.ExportedTo, "pricing", "tiers.md")); err != nil {
		t.Fatal(err)
	}
}

func TestCompileWavesAndRepoTarget(t *testing.T) {
	p, h := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	var titles []string
	for i := range app.MaxCompileParallel + 2 {
		titles = append(titles, fmt.Sprintf("Branch %d", i+1))
	}
	addKids(t, p, m, m.RootID, "", titles...)
	runs := apiRuns(t, p, "/api/maps/"+m.ID+"/compile", map[string]any{"node_id": m.RootID, "profile_id": "prof-claude", "repo_id": "repo-1"})
	if len(runs) != 1+app.MaxCompileParallel || len(h.Created()) != app.MaxCompileParallel {
		t.Fatalf("first wave: %d runs, %d tasks", len(runs), len(h.Created()))
	}
	for _, in := range h.Created() {
		if len(in.Repositories) != 0 {
			t.Fatal("section tasks need no repository")
		}
	}
	// A section that finishes makes room for the next one.
	tool(t, p, runs[1].TaskID, "report", map[string]any{"summary": "Done.", "status": "done"})
	state(p, runs[1].TaskID, "WAITING_FOR_INPUT")
	if len(h.Created()) != app.MaxCompileParallel+1 {
		t.Fatalf("second wave: %d tasks", len(h.Created()))
	}
	state(p, runs[2].TaskID, "FAILED")
	if len(h.Created()) != app.MaxCompileParallel+2 {
		t.Fatalf("last section: %d tasks", len(h.Created()))
	}
	for i := 3; i <= app.MaxCompileParallel; i++ {
		state(p, runs[i].TaskID, "COMPLETED")
	}
	for _, tid := range []string{"task-9", "task-10"} {
		state(p, tid, "COMPLETED")
	}
	// The synthesis gets the repository and writes docs/mindmaps on its branch.
	if len(h.Created()) != app.MaxCompileParallel+3 {
		t.Fatalf("synthesis: %d tasks", len(h.Created()))
	}
	last := h.Created()[app.MaxCompileParallel+2]
	if len(last.Repositories) != 1 || last.Repositories[0].RepositoryID != "repo-1" || !strings.Contains(*last.Launch.Prompt, "docs/mindmaps/product-launch.md") {
		t.Fatalf("synthesis task: %+v", last.Repositories)
	}
	// A plain run cannot use the compile templates.
	if st, b := api(t, p, "POST", "/api/maps/"+m.ID+"/runs", map[string]any{"node_id": m.RootID, "template": "compile", "profile_id": "prof-claude"}); st != 400 {
		t.Fatalf("POST runs compile: %d %s", st, b)
	}
}

func TestCompileGuestApprovalAndCaps(t *testing.T) {
	p, h := newTestPluginHost(t)
	m, _ := compileMap(t, p)
	if st, b := api(t, p, "PUT", "/api/settings/share", map[string]any{"provider": "cloudflared", "max_concurrent_runs": 5, "require_guest_run_approval": true}); st != 200 {
		t.Fatalf("share settings: %d %s", st, b)
	}
	guest, err := p.store.CreateGuest(m.ID, "Bea", "")
	if err != nil {
		t.Fatal(err)
	}
	runs, err := p.svc.StartCompile(context.Background(), app.CompileSpec{MapID: m.ID, NodeID: m.RootID, ProfileID: "prof-claude", StartedBy: guest.ID})
	if err != nil || len(runs) != 4 || len(h.Created()) != 0 {
		t.Fatalf("guest compile: %d runs, %d tasks, %v", len(runs), len(h.Created()), err)
	}
	for _, r := range runs[1:] {
		if r.Status != contract.RunWaiting || r.WaitingReason != contract.WaitingApproval || r.TaskID != "" {
			t.Fatalf("section not held: %+v", r)
		}
	}
	if c := getRun(t, p, runs[0].ID); c.Status != contract.RunWaiting || c.WaitingReason != contract.WaitingApproval {
		t.Fatalf("coordinator: %s %s", c.Status, c.WaitingReason)
	}
	approved := apiRuns(t, p, "/api/runs/"+runs[0].ID+"/approve", nil)
	if len(approved) != 3 || len(h.Created()) != 3 {
		t.Fatalf("approve: %d runs, %d tasks", len(approved), len(h.Created()))
	}
	// Once approved, the synthesis is not held again.
	for _, r := range approved {
		state(p, r.TaskID, "COMPLETED")
	}
	if len(h.Created()) != 4 {
		t.Fatalf("synthesis after approval: %d tasks", len(h.Created()))
	}
	if s, _ := p.store.RunByTask("task-4"); s.Status == contract.RunWaiting {
		t.Fatalf("synthesis held: %+v", s)
	}
}

func TestCompileQueuesWithinSharedRunCap(t *testing.T) {
	p, h := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	host, err := p.store.HostParticipant(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.store.InsertShare(store.NewID(contract.PrefixShare), m.ID, "cloudflared", host.ID); err != nil {
		t.Fatal(err)
	}
	branches := addKids(t, p, m, m.RootID, "", "One", "Two", "Three", "Four", "Five", "Six")
	// A shared map allows 4 concurrent runs: 4 of 6 sections start, the rest queue.
	runs := apiRuns(t, p, "/api/maps/"+m.ID+"/compile", map[string]any{"node_id": m.RootID, "profile_id": "prof-claude"})
	if len(runs) != 5 || len(h.Created()) != 4 {
		t.Fatalf("first wave: %d runs, %d tasks", len(runs), len(h.Created()))
	}
	// No slot free: another compile is refused before it writes anything.
	docs := len(docNodes(t, p, m))
	st, b := api(t, p, "POST", "/api/maps/"+m.ID+"/compile", map[string]any{"node_id": branches[0], "profile_id": "prof-claude"})
	if st != 403 || !strings.Contains(string(b), string(contract.ErrBudgetExceeded)) || len(h.Created()) != 4 || len(docNodes(t, p, m)) != docs {
		t.Fatalf("compile with no free slot: %d %s", st, b)
	}
	// A section that ends frees its slot for the next one.
	state(p, runs[1].TaskID, "COMPLETED")
	if len(h.Created()) != 5 {
		t.Fatalf("after one section ended: %d tasks", len(h.Created()))
	}
	state(p, runs[2].TaskID, "FAILED")
	if len(h.Created()) != 6 {
		t.Fatalf("after two sections ended: %d tasks", len(h.Created()))
	}
	plan := map[string]bool{}
	for i := range h.Created() {
		r, _ := p.store.RunByTask(fmt.Sprintf("task-%d", i+1))
		plan[r.NodeID] = true
	}
	for _, id := range branches {
		if !plan[id] {
			t.Fatalf("section %s never started", id)
		}
	}
	// The synthesis waits for every section.
	for _, tid := range []string{runs[3].TaskID, runs[4].TaskID, "task-5"} {
		state(p, tid, "COMPLETED")
		if len(h.Created()) != 6 {
			t.Fatalf("synthesis started early: %d tasks", len(h.Created()))
		}
	}
	state(p, "task-6", "COMPLETED")
	if len(h.Created()) != 7 {
		t.Fatalf("synthesis not started: %d tasks", len(h.Created()))
	}
	if s, err := p.store.RunByTask("task-7"); err != nil || s.Template != contract.TemplateSynthesis || s.ParentRunID != runs[0].ID {
		t.Fatalf("synthesis run: %+v %v", s, err)
	}
}

func TestRecompileUpdatesDocTree(t *testing.T) {
	p, h := newTestPluginHost(t)
	h.config["export_dir"] = t.TempDir()
	m, branches := compileMap(t, p)
	finish := func(runs []contract.Run) {
		t.Helper()
		for _, r := range runs[1:] {
			state(p, r.TaskID, "COMPLETED")
		}
		synth, _ := p.store.RunByTask(fmt.Sprintf("task-%d", len(h.Created())))
		if synth.Template != contract.TemplateSynthesis {
			t.Fatalf("no synthesis: %+v", synth)
		}
		state(p, synth.TaskID, "COMPLETED")
		if c := getRun(t, p, runs[0].ID); c.Status != contract.RunDone {
			t.Fatalf("compile: %s", c.Status)
		}
	}
	byPath := func() map[string]contract.Node {
		out := map[string]contract.Node{}
		for _, d := range docNodes(t, p, m) {
			if _, dup := out[d.Doc.Path]; dup {
				t.Fatalf("two docs at %s", d.Doc.Path)
			}
			out[d.Doc.Path] = d
		}
		return out
	}
	first := apiRuns(t, p, "/api/maps/"+m.ID+"/compile", map[string]any{"node_id": m.RootID, "profile_id": "prof-claude"})
	before := byPath()
	pricing := before["pricing/index.md"]
	if res := tool(t, p, first[1].TaskID, "write_doc", map[string]any{"doc_id": pricing.ID, "title": "Pricing", "path": "pricing/index.md", "body": "Old pricing."}); res.IsError {
		t.Fatal(res.Text)
	}
	if res := tool(t, p, first[1].TaskID, "write_doc", map[string]any{"parent_doc_id": pricing.ID, "title": "Free tier", "path": "pricing/free-tier.md", "body": "Old free."}); res.IsError {
		t.Fatal(res.Text)
	}
	// Recompiling while the first compile runs would race its agents.
	if st, b := api(t, p, "POST", "/api/maps/"+m.ID+"/compile", map[string]any{"node_id": m.RootID, "profile_id": "prof-claude"}); st != 409 {
		t.Fatalf("recompile during compile: %d %s", st, b)
	}
	finish(first)
	before = byPath()

	// The branch changes: Risks is renamed, a branch is added.
	host, _ := p.store.HostParticipant(m.ID)
	ttl := "Risks and mitigations"
	if _, err := p.svc.Submit(m.ID, []contract.Op{{ID: store.NewID(contract.PrefixOp), MapID: m.ID, Type: contract.OpNodeUpdate,
		Payload: &contract.NodeUpdatePayload{ID: branches[2], Set: &contract.NodeFields{Title: &ttl}}}}, app.SubmitOptions{Actor: contract.Actor{ParticipantID: host.ID}}); err != nil {
		t.Fatal(err)
	}
	addKids(t, p, m, m.RootID, "", "Launch plan")
	second := apiRuns(t, p, "/api/maps/"+m.ID+"/compile", map[string]any{"node_id": m.RootID, "profile_id": "prof-claude"})
	if len(second) != 5 {
		t.Fatalf("second compile: %d runs", len(second))
	}
	after := byPath()
	trees, _ := p.svc.DocTrees(m.ID)
	if len(trees) != 1 {
		t.Fatalf("doc trees after recompile: %d", len(trees))
	}
	for _, path := range []string{"index.md", "product-launch.md", "pricing/index.md", "research/index.md", "pricing/free-tier.md"} {
		if after[path].ID != before[path].ID {
			t.Fatalf("%s was recreated: %s -> %s", path, before[path].ID, after[path].ID)
		}
	}
	// Renamed: same doc (matched by its source branch), new title and path.
	risks := after["risks-and-mitigations/index.md"]
	if risks.ID != before["risks/index.md"].ID || risks.Title != ttl {
		t.Fatalf("renamed section doc: %+v", risks)
	}
	if _, ok := after["risks/index.md"]; ok {
		t.Fatal("old path kept")
	}
	if launch, ok := after["launch-plan/index.md"]; !ok || launch.ParentID != after["index.md"].ID {
		t.Fatalf("new section doc: %+v", launch)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("docs: %d before, %d after", len(before), len(after))
	}
	// The section agent is pointed at the existing docs and updates them.
	pr := prompt(h, len(h.Created())-4)
	if !strings.Contains(pr, `doc_id: "`+pricing.ID+`"`) || !strings.Contains(pr, `exists: `+"`"+`doc_id: "`+before["pricing/free-tier.md"].ID+`"`) {
		t.Fatalf("recompile prompt:\n%s", pr)
	}
	if res := tool(t, p, second[1].TaskID, "write_doc", map[string]any{"doc_id": pricing.ID, "title": "Pricing v2", "path": "pricing/index.md", "body": "New pricing."}); res.IsError {
		t.Fatal(res.Text)
	}
	if d := byPath()["pricing/index.md"]; d.ID != pricing.ID || d.Title != "Pricing v2" || d.BodyMD != "New pricing." {
		t.Fatalf("updated section doc: %+v", d)
	}
	finish(second)
	if trees, _ := p.svc.DocTrees(m.ID); len(trees) != 1 || trees[0].ExportedTo == "" {
		t.Fatalf("doc trees: %+v", trees)
	}
}

func TestProposeCompileWritesDocsLive(t *testing.T) {
	p, h := newTestPluginHost(t)
	h.config["export_dir"] = t.TempDir()
	m, branches := compileMap(t, p)
	runs := apiRuns(t, p, "/api/maps/"+m.ID+"/compile", map[string]any{"node_id": m.RootID, "profile_id": "prof-claude", "mode": "propose"})
	if runs[1].Mode != contract.ModePropose || !strings.Contains(prompt(h, 0), "Propose mode covers your other edits only") {
		t.Fatalf("section run: %+v", runs[1])
	}
	var pricing contract.Node
	for _, d := range docNodes(t, p, m) {
		if d.Doc.Path == "pricing/index.md" {
			pricing = d
		}
	}
	if res := tool(t, p, runs[1].TaskID, "write_doc", map[string]any{"doc_id": pricing.ID, "title": "Pricing", "path": "pricing/index.md",
		"body": "Three tiers and a student discount."}); res.IsError {
		t.Fatal(res.Text)
	}
	if res := tool(t, p, runs[1].TaskID, "write_doc", map[string]any{"parent_doc_id": pricing.ID, "title": "Tiers", "path": "pricing/tiers.md", "body": "Free, Pro, Team."}); res.IsError {
		t.Fatal(res.Text)
	}
	// Other edits are still proposals.
	if res := tool(t, p, runs[1].TaskID, "add", map[string]any{"parent_id": branches[0], "nodes": []map[string]any{{"title": "Enterprise tier"}}}); res.IsError {
		t.Fatal(res.Text)
	}
	snap, _ := p.store.Snapshot(m.ID)
	for _, n := range snap.Nodes {
		if n.Title == "Enterprise tier" {
			t.Fatal("a propose-mode add was applied")
		}
	}
	// A doc outside the compile's tree is proposed too.
	if res := tool(t, p, runs[1].TaskID, "write_doc", map[string]any{"title": "Stray", "path": "stray.md", "body": "x"}); res.IsError {
		t.Fatal(res.Text)
	}
	for _, d := range docNodes(t, p, m) {
		if d.Title == "Stray" {
			t.Fatal("a doc outside the compile tree was applied")
		}
	}
	for _, r := range runs[1:] {
		state(p, r.TaskID, "COMPLETED")
	}
	synth, err := p.store.RunByTask("task-4")
	if err != nil || synth.Template != contract.TemplateSynthesis || synth.Mode != contract.ModePropose {
		t.Fatalf("synthesis: %+v %v", synth, err)
	}
	var root contract.Node
	for _, d := range docNodes(t, p, m) {
		if d.ParentID == m.RootID {
			root = d
		}
	}
	res := tool(t, p, synth.TaskID, "read", map[string]any{"node_id": root.ID, "depth": 4, "include_bodies": true})
	if res.IsError || !strings.Contains(res.Text, "Three tiers and a student discount.") || !strings.Contains(res.Text, "Free, Pro, Team.") {
		t.Fatalf("synthesis read:\n%s", res.Text)
	}
	if res := tool(t, p, synth.TaskID, "write_doc", map[string]any{"doc_id": root.ID, "title": "Overview", "path": "index.md", "body": "# Overview"}); res.IsError {
		t.Fatal(res.Text)
	}
	if n, _ := p.store.Node(m.ID, root.ID); n.BodyMD != "# Overview" {
		t.Fatalf("synthesis doc not live: %q", n.BodyMD)
	}
}

func TestSuggestProposesNodes(t *testing.T) {
	p, h := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	pricing := addKids(t, p, m, m.RootID, "", "Pricing")[0]
	addKids(t, p, m, pricing, "", "Free tier")
	var gotPrompt, gotProfile string
	h.utility = func(prompt, profile string) (string, error) {
		gotPrompt, gotProfile = prompt, profile
		return "Here you go:\n```json\n[{\"title\":\"Student discount\",\"kind\":\"idea\",\"body\":\"Half price with a school email.\"}," +
			"{\"title\":\"Free tier\",\"kind\":\"idea\"},{\"title\":\"Churn risk\",\"kind\":\"risk\"},{\"title\":\"Odd one\",\"kind\":\"doc\"}]\n```", nil
	}
	st, b := api(t, p, "POST", "/api/maps/"+m.ID+"/suggest", map[string]any{"node_id": pricing, "count": 5})
	var res contract.SuggestResponse
	if err := json.Unmarshal(b, &res); st != 200 || err != nil || res.Status != contract.SuggestDone {
		t.Fatalf("suggest: %d %s", st, b)
	}
	if !strings.Contains(gotPrompt, `Node: "Pricing"`) || !strings.Contains(gotPrompt, "- Free tier") || gotProfile != "" {
		t.Fatalf("utility prompt/profile: %q %q", gotPrompt, gotProfile)
	}
	// The existing "Free tier" is dropped; an unknown kind becomes an idea.
	if len(res.Suggestions) != 3 || res.Suggestions[0].Title != "Student discount" || res.Suggestions[1].Kind != contract.KindRisk ||
		res.Suggestions[2].Kind != contract.KindIdea || len(res.NodeIDs) != 3 || res.ProposalID == "" {
		t.Fatalf("suggestions: %+v", res)
	}
	// Proposed, not applied: ghosts until accepted.
	if _, err := p.store.Node(m.ID, res.NodeIDs[0]); err == nil {
		t.Fatal("suggestion applied before review")
	}
	prop, _ := p.store.GetProposal(res.ProposalID)
	if prop.Status != contract.ProposalReady || len(prop.Items) != 3 {
		t.Fatalf("proposal: %+v", prop)
	}
	if st, b := api(t, p, "POST", "/api/proposals/"+res.ProposalID+"/review", map[string]any{"accept": []string{prop.Items[0].Op.ID}}); st != 200 {
		t.Fatalf("review: %d %s", st, b)
	}
	n, err := p.store.Node(m.ID, res.NodeIDs[0])
	if err != nil || n.ParentID != pricing || !slices.Contains(n.Tags, app.SuggestTag) || n.BodyMD != "Half price with a school email." {
		t.Fatalf("accepted suggestion: %+v %v", n, err)
	}
	if r := getRun(t, p, res.JobID); r.Template != contract.TemplateSuggest || r.Status != contract.RunDone || r.TaskID != "" || len(h.Created()) != 0 {
		t.Fatalf("suggest run: %+v", r)
	}

	// A slow utility agent: 202, then poll the job.
	old := web.SuggestWait
	web.SuggestWait = 20 * time.Millisecond
	t.Cleanup(func() { web.SuggestWait = old })
	release := make(chan struct{})
	h.utility = func(string, string) (string, error) {
		<-release
		return `[{"title":"Annual plan"}]`, nil
	}
	st, b = api(t, p, "POST", "/api/maps/"+m.ID+"/suggest", map[string]any{"node_id": pricing, "profile_id": "prof-claude"})
	if err := json.Unmarshal(b, &res); st != 202 || err != nil || res.Status != contract.SuggestRunning || res.JobID == "" {
		t.Fatalf("slow suggest: %d %s", st, b)
	}
	close(release)
	waitFor(t, func() bool {
		_, b := api(t, p, "GET", "/api/maps/"+m.ID+"/suggest/"+res.JobID, nil)
		var r contract.SuggestResponse
		return json.Unmarshal(b, &r) == nil && r.Status == contract.SuggestDone && len(r.Suggestions) == 1
	})
	if st, b = api(t, p, "POST", "/api/maps/"+m.ID+"/suggest", map[string]any{"node_id": pricing, "count": 99}); st != 413 {
		t.Fatalf("count over max: %d %s", st, b)
	}
}

func TestSuggestFailureAndGuestHold(t *testing.T) {
	p, h := newTestPluginHost(t)
	m := newMapViaAPI(t, p)
	h.utility = func(string, string) (string, error) { return "", fmt.Errorf("agent profile is not eligible") }
	run, done, err := p.svc.StartSuggest(context.Background(), m.ID, m.RootID, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	<-done
	_, res, _ := p.svc.SuggestResult(run.ID)
	if res.Status != contract.SuggestFailed || !strings.Contains(res.Error, "not eligible") {
		t.Fatalf("failed suggest: %+v", res)
	}
	h.utility = func(string, string) (string, error) { return "no json here", nil }
	run, done, _ = p.svc.StartSuggest(context.Background(), m.ID, m.RootID, 3, "", "")
	<-done
	if _, res, _ = p.svc.SuggestResult(run.ID); res.Status != contract.SuggestFailed {
		t.Fatalf("unusable reply: %+v", res)
	}
	if st, b := api(t, p, "PUT", "/api/settings/share", map[string]any{"provider": "cloudflared", "require_guest_run_approval": true}); st != 200 {
		t.Fatalf("share settings: %d %s", st, b)
	}
	guest, _ := p.store.CreateGuest(m.ID, "Bea", "")
	if _, _, err := p.svc.StartSuggest(context.Background(), m.ID, m.RootID, 3, "", guest.ID); contract.AsError(err).Code != contract.ErrForbidden {
		t.Fatalf("guest suggest under approval: %v", err)
	}
}
