package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kandev/kandev/pkg/pluginsdk"
	"google.golang.org/protobuf/types/known/structpb"

	"kandev-plugin-mindmap/server/app"
	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/store"
)

type recMessenger struct {
	mu   sync.Mutex
	sent []string
}

func (r *recMessenger) Send(_ context.Context, _, _, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, text)
	return nil
}

func newTestPlugin(t *testing.T) *mindmapPlugin {
	p, _ := newTestPluginHost(t)
	return p
}

// newTestPluginHost is a plugin wired to a fake Kandev host: the real
// launcher, profile lookup and messenger run against it.
func newTestPluginHost(t *testing.T) (*mindmapPlugin, *fakeHost) {
	t.Helper()
	t.Setenv("KANDEV_PLUGIN_DATA_DIR", t.TempDir())
	p := newPlugin()
	if p.initErr != nil {
		t.Fatal(p.initErr)
	}
	t.Cleanup(p.close)
	h := newFakeHost()
	p.SetHost(h)
	return p, h
}

func webhook(t *testing.T, p *mindmapPlugin, key, method, query string, body any) (int32, []byte) {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	res, err := p.HandleWebhook(context.Background(), &pluginsdk.WebhookRequest{WebhookKey: key, Method: method, Query: query, Body: b,
		Headers: map[string]string{"Origin": "http://127.0.0.1:38430"}})
	if err != nil {
		t.Fatal(err)
	}
	return res.Status, res.Body
}

func newMapViaAPI(t *testing.T, p *mindmapPlugin) contract.Map {
	t.Helper()
	st, body := webhook(t, p, "api", "POST", "", map[string]any{"method": "POST", "path": "/api/maps",
		"body": map[string]any{"workspace_id": "ws-1", "title": "Product launch"}})
	var m contract.Map
	if err := json.Unmarshal(body, &m); st != 200 || err != nil || m.RootID == "" {
		t.Fatalf("api create map: %d %s", st, body)
	}
	return m
}

func TestWebhooksSessionApiSyncAsset(t *testing.T) {
	p := newTestPlugin(t)
	m := newMapViaAPI(t, p)
	// api proxies reads too.
	st, body := webhook(t, p, "api", "POST", "", map[string]any{"method": "GET", "path": "/api/maps/" + m.ID + "/snapshot"})
	var snap contract.Snapshot
	if err := json.Unmarshal(body, &snap); st != 200 || err != nil || len(snap.Nodes) != 1 {
		t.Fatalf("api snapshot: %d %s", st, body)
	}
	if st, _ := webhook(t, p, "api", "POST", "", map[string]any{"method": "GET", "path": "/m/x/../../agent/v1/tools"}); st != 400 {
		t.Fatalf("api path escape: %d", st)
	}
	// session issues a local token that opens the loopback WebSocket.
	st, body = webhook(t, p, "session", "POST", "", map[string]any{"map_id": m.ID})
	var sess contract.SessionResponse
	if err := json.Unmarshal(body, &sess); st != 200 || err != nil || !strings.HasPrefix(sess.Token, "mml1.") || !sess.Participant.IsHost {
		t.Fatalf("session: %d %s", st, body)
	}
	u := strings.Replace(sess.BaseURL, "http://", "ws://", 1) + "/m/" + sess.Token + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(u, map[string][]string{"Origin": {"http://127.0.0.1:38430"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	hello, _ := json.Marshal(contract.Message{Type: contract.MsgHello, Payload: &contract.HelloPayload{Contract: 1, ClientID: "tab"}})
	_ = conn.WriteMessage(websocket.TextMessage, hello)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, frame, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(frame), `"type":"welcome"`) {
		t.Fatalf("ws welcome: %s %v", frame, err)
	}
	// sync: first call is welcome+snapshot; a held call returns empty after wait.
	st, body = webhook(t, p, "sync", "GET", "map_id="+m.ID, nil)
	var sr contract.SyncResponse
	if err := json.Unmarshal(body, &sr); st != 200 || err != nil || len(sr.Messages) != 2 {
		t.Fatalf("sync: %d %s", st, body)
	}
	start := time.Now()
	st, body = webhook(t, p, "sync", "GET", "map_id="+m.ID+"&wait=1&cursor="+sr.Cursor, nil)
	if st != 200 || time.Since(start) < 900*time.Millisecond || !strings.Contains(string(body), `"messages":[]`) {
		t.Fatalf("held sync: %d %s after %v", st, body, time.Since(start))
	}
	// submit webhook.
	op := contract.Op{ID: store.NewID("op"), MapID: m.ID, Type: contract.OpNodeCreate, Payload: &contract.NodeCreatePayload{
		Node: contract.NodeInit{ID: store.NewID("nd"), ParentID: m.RootID, Order: "V", Title: "Child", Kind: contract.KindIdea}}}
	st, body = webhook(t, p, "submit", "POST", "map_id="+m.ID, contract.SubmitRequest{ClientID: "c", Ops: []contract.Op{op}})
	if st != 200 || !strings.Contains(string(body), `"type":"ack"`) {
		t.Fatalf("submit: %d %s", st, body)
	}
	// asset webhook serves stored bytes.
	a, err := p.store.StoreBlob([]byte("plain notes"), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	a.NodeID = m.RootID
	att := contract.Op{ID: store.NewID("op"), MapID: m.ID, Type: contract.OpAssetAttach, Payload: &contract.AssetAttachPayload{Asset: a}}
	if _, err := p.svc.Submit(m.ID, []contract.Op{att}, app.SubmitOptions{Actor: contract.Actor{ParticipantID: sess.Participant.ID}}); err != nil {
		t.Fatal(err)
	}
	res, _ := p.HandleWebhook(context.Background(), &pluginsdk.WebhookRequest{WebhookKey: "asset", Query: "map_id=" + m.ID + "&id=" + a.ID})
	if res.Status != 200 || string(res.Body) != "plain notes" || res.Headers["Content-Type"] != "text/plain" {
		t.Fatalf("asset: %d %q %v", res.Status, res.Body, res.Headers)
	}
	if res, _ := p.HandleWebhook(context.Background(), &pluginsdk.WebhookRequest{WebhookKey: "asset", Query: "map_id=" + m.ID + "&id=as_0000000000000000"}); res.Status != 404 {
		t.Fatalf("missing asset: %d", res.Status)
	}
	// Removed spike webhooks are gone.
	for _, k := range []string{"debug-launch", "probe", "hold", "echo-size", "tunnel-test", "task-ops", "logs"} {
		if st, _ := webhook(t, p, k, "GET", "", nil); st != 404 {
			t.Errorf("%s still served: %d", k, st)
		}
	}
}

func startRun(t *testing.T, p *mindmapPlugin, m contract.Map, transport contract.ToolTransport) store.RunRecord {
	t.Helper()
	host, _ := p.store.HostParticipant(m.ID)
	r, err := p.svc.StartRun(context.Background(), app.RunSpec{NewRun: store.NewRun{MapID: m.ID, NodeID: m.RootID, ProfileID: "prof-claude", Transport: transport, StartedBy: host.ID}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAgentToolsThroughKandev(t *testing.T) {
	p := newTestPlugin(t)
	m := newMapViaAPI(t, p)
	res, err := p.InvokeAgentTool(context.Background(), &pluginsdk.AgentToolRequest{Name: "context", Arguments: map[string]any{},
		Context: pluginsdk.AgentToolContext{TaskID: "some-other-task", Surface: "kanban-task"}})
	if err != nil || !res.IsError || !strings.HasPrefix(res.Text, "error not_a_run:") {
		t.Fatalf("unrelated task: %+v %v", res, err)
	}
	run := startRun(t, p, m, contract.TransportMCP)
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"context", map[string]any{}},
		{"add", map[string]any{"nodes": []any{map[string]any{"title": "Beta users", "ref": "beta"}}}},
		{"read", map[string]any{"depth": 2}},
		{"spawn", map[string]any{"agents": []any{map[string]any{"node_id": "@beta", "goal": "Find 3 examples"}}}},
	} {
		res, err := p.InvokeAgentTool(context.Background(), &pluginsdk.AgentToolRequest{Name: call.name, Arguments: call.args,
			Context: pluginsdk.AgentToolContext{TaskID: run.TaskID}})
		if err != nil || res.IsError || res.Text == "" {
			t.Fatalf("%s: %+v %v", call.name, res, err)
		}
		// Structured content must survive the protobuf Struct conversion.
		if res.StructuredContent != nil {
			if _, err := structpb.NewStruct(res.StructuredContent); err != nil {
				t.Fatalf("%s structured content not protobuf-safe: %v", call.name, err)
			}
		}
	}
}

func event(typ string, payload map[string]any) *pluginsdk.Event {
	return &pluginsdk.Event{EventID: store.NewID("ev"), EventType: typ, Payload: payload}
}

func TestEventsDriveRunStatus(t *testing.T) {
	p := newTestPlugin(t)
	m := newMapViaAPI(t, p)
	run := startRun(t, p, m, contract.TransportMCP)
	status := func() store.RunRecord {
		r, _ := p.store.GetRun(run.ID)
		return r
	}
	ctx := context.Background()
	_ = p.OnEvent(ctx, event("task_session.state_changed", map[string]any{"task_id": run.TaskID, "session_id": "s-1", "new_state": "RUNNING"}))
	if r := status(); r.Status != contract.RunRunning || r.SessionID == "" {
		t.Fatalf("running: %+v", r.Run)
	}
	if n, _ := p.store.Node(m.ID, m.RootID); n.Status != contract.StatusRunning || n.RunID != run.ID {
		t.Fatalf("node ring %s run %s", n.Status, n.RunID)
	}
	_ = p.OnEvent(ctx, event("task_session.state_changed", map[string]any{"task_id": run.TaskID})) // no new_state: ignored
	if r := status(); r.Status != contract.RunRunning {
		t.Fatalf("missing new_state changed status to %s", r.Status)
	}
	_ = p.OnEvent(ctx, event("task_session.state_changed", map[string]any{"task_id": run.TaskID, "new_state": "WAITING_FOR_INPUT"}))
	if r := status(); r.Status != contract.RunWaiting || r.WaitingReason != contract.WaitingTurnEnded {
		t.Fatalf("turn ended: %s/%s", r.Status, r.WaitingReason)
	}
	_ = p.OnEvent(ctx, event("task_session.state_changed", map[string]any{"task_id": run.TaskID, "new_state": "RUNNING"}))
	res, _ := p.InvokeAgentTool(ctx, &pluginsdk.AgentToolRequest{Name: "report", Arguments: map[string]any{"summary": "All done.", "status": "review"},
		Context: pluginsdk.AgentToolContext{TaskID: run.TaskID}})
	if res.IsError {
		t.Fatal(res.Text)
	}
	_ = p.OnEvent(ctx, event("task_session.state_changed", map[string]any{"task_id": run.TaskID, "new_state": "IDLE"}))
	if r := status(); r.Status != contract.RunReview {
		t.Fatalf("after report: %s", r.Status)
	}
	if n, _ := p.store.Node(m.ID, m.RootID); n.Status != contract.StatusReview {
		t.Fatalf("node ring after report: %s", n.Status)
	}
	// Agent output becomes the run's last_output.
	_ = p.OnEvent(ctx, event("message.added", map[string]any{"task_id": run.TaskID, "message_id": "m1", "author_type": "agent", "content": "Looking at pricing pages"}))
	if r := status(); r.LastOutput != "Looking at pricing pages" {
		t.Fatalf("last output %q", r.LastOutput)
	}
	_ = p.OnEvent(ctx, event("task_session.state_changed", map[string]any{"task_id": run.TaskID, "new_state": "FAILED"}))
	if r := status(); r.Status != contract.RunFailed || r.EndedAt.IsZero() {
		t.Fatalf("failed: %+v", r.Run)
	}
	// Tasks that are not runs are ignored.
	if err := p.OnEvent(ctx, event("task_session.state_changed", map[string]any{"task_id": "unrelated", "new_state": "RUNNING"})); err != nil {
		t.Fatal(err)
	}
}

func TestPatchBlocksFromMessages(t *testing.T) {
	p := newTestPlugin(t)
	msgr := &recMessenger{}
	p.svc.Messenger = msgr
	m := newMapViaAPI(t, p)
	run := startRun(t, p, m, contract.TransportPrompt)
	content := "Here is my research.\n\n```mindmap-patch\n" +
		`{"v":1,"calls":[{"tool":"add","args":{"nodes":[{"ref":"tiers","title":"Three-tier freemium","children":[{"title":"Example Co","kind":"example","sources":[{"url":"https://example.com/pricing"}]}]}]}},` +
		`{"tool":"update","args":{"node_id":"@tiers","append_body":"Free up to 3 notebooks."}}]}` +
		"\n```\n\nAnd a bad one:\n\n```mindmap-patch\n" +
		`{"v":1,"calls":[{"tool":"link","args":{"from_id":"@tiers","to_id":"@missing","label":"x"}}]}` +
		"\n```\n\nStill typing:\n```mindmap-patch\n{\"v\":1"
	ev := event("message.added", map[string]any{"task_id": run.TaskID, "message_id": "msg-1", "author_type": "agent", "content": content})
	_ = p.OnEvent(context.Background(), ev)
	_ = p.OnEvent(context.Background(), ev) // re-delivery is a no-op
	snap, _ := p.store.Snapshot(m.ID)
	if len(snap.Nodes) != 3 {
		t.Fatalf("nodes after patch: %d", len(snap.Nodes))
	}
	tiers, _ := p.store.ResolveRef(run.RootRunID, "tiers")
	if n, _ := p.store.Node(m.ID, tiers); n.BodyMD != "Free up to 3 notebooks." {
		t.Fatalf("update via patch: %q", n.BodyMD)
	}
	msgr.mu.Lock()
	defer msgr.mu.Unlock()
	if len(msgr.sent) != 1 || !strings.Contains(msgr.sent[0], "@missing") && !strings.Contains(msgr.sent[0], "missing") || !strings.Contains(msgr.sent[0], "tiers") {
		t.Fatalf("feedback messages: %q", msgr.sent)
	}
	// Read-only tools are refused in patches.
	if _, err := contract.ParsePatch([]byte(`{"v":1,"calls":[{"tool":"read","args":{}}]}`)); err == nil {
		t.Fatal("read allowed in patch")
	}
}
