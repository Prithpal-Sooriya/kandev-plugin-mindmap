package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"kandev-plugin-mindmap/server/app"
	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/hub"
	"kandev-plugin-mindmap/server/store"
	"kandev-plugin-mindmap/server/tools"
)

type env struct {
	t     *testing.T
	st    *store.Store
	svc   *app.Service
	srv   *Server
	http  *httptest.Server
	m     contract.Map
	host  contract.Participant
	token string // local token scoped to m
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := hub.New()
	svc := app.New(st, h)
	svc.Launcher = &app.FakeLauncher{}
	d, _ := tools.NewDispatcher(svc)
	srv := New(svc, d)
	hs := httptest.NewServer(srv.Handler())
	svc.SetPort(hs.Listener.Addr().(*net.TCPAddr).Port)
	t.Cleanup(func() { hs.Close(); h.Close(); _ = st.Close() })
	m, host, err := st.CreateMap(contract.CreateMapRequest{WorkspaceID: "ws-1", Title: "Launch plan"})
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := st.IssueToken(contract.TokenLocal, 0, m.ID, "", host.ID, "", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, st: st, svc: svc, srv: srv, http: hs, m: m, host: host, token: tok}
}

func (e *env) do(method, path string, body any, hdr map[string]string) (*http.Response, []byte) {
	e.t.Helper()
	var rb io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rb = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.http.URL+path, rb)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func createOp(mapID, id, parent, title string) contract.Op {
	return contract.Op{ID: store.NewID(contract.PrefixOp), MapID: mapID, Type: contract.OpNodeCreate, Payload: &contract.NodeCreatePayload{
		Node: contract.NodeInit{ID: id, ParentID: parent, Order: "V", Title: title, Kind: contract.KindIdea}}}
}

func TestTokenProblemsAre404OnGuestRoutes(t *testing.T) {
	e := newEnv(t)
	other, _, _ := e.st.CreateMap(contract.CreateMapRequest{WorkspaceID: "ws-1", Title: "Other"})
	share, _, _ := e.st.IssueToken(contract.TokenShare, 0, e.m.ID, "", "", "", "", 0)
	revoked, info, _ := e.st.IssueToken(contract.TokenLocal, 0, e.m.ID, "", e.host.ID, "", "", time.Hour)
	_ = e.st.RevokeToken(info.ID)
	run, _ := e.svc.StartRun(context.Background(), store.NewRun{MapID: e.m.ID, NodeID: e.m.RootID, StartedBy: e.host.ID})
	runTok := e.svc.Launcher.(*app.FakeLauncher).Launches[0].RunToken
	_ = run
	cases := []struct {
		name, path string
		hdr        map[string]string
		want       int
	}{
		{"no token", "/m/", nil, 404},
		{"garbage token", "/m/nonsense/", nil, 404},
		{"wellformed unknown", "/m/mml1.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/api/maps", nil, 404},
		{"revoked", "/m/" + revoked + "/api/maps", nil, 404},
		{"run token on guest route", "/m/" + runTok + "/api/maps", nil, 404},
		{"share token, other map", "/m/" + share + "/api/maps/" + other.ID + "/snapshot", nil, 404},
		{"local token via tunnel", "/m/" + e.token + "/api/maps", map[string]string{"Cf-Connecting-Ip": "203.0.113.9"}, 404},
		{"local token, foreign origin", "/m/" + e.token + "/api/maps", map[string]string{"Origin": "https://evil.example"}, 404},
		{"local token, other map", "/m/" + e.token + "/api/maps/" + other.ID + "/snapshot", nil, 403},
		{"share token, own map", "/m/" + share + "/api/maps/" + e.m.ID + "/snapshot", nil, 200},
		{"local token", "/m/" + e.token + "/api/maps/" + e.m.ID + "/snapshot", map[string]string{"Origin": "http://127.0.0.1:38430"}, 200},
		{"static app", "/m/" + e.token + "/", nil, 200},
		{"bridge without token", "/agent/v1/tools", nil, 401},
		{"bridge via tunnel", "/agent/v1/tools", map[string]string{"Authorization": "Bearer " + runTok, "X-Forwarded-For": "203.0.113.9"}, 404},
		{"bridge wrong contract", "/agent/v1/tools", map[string]string{"Authorization": "Bearer " + runTok, "X-Mindmap-Contract": "2"}, 426},
		{"bridge", "/agent/v1/tools", map[string]string{"Authorization": "Bearer " + runTok}, 200},
	}
	for _, c := range cases {
		resp, body := e.do("GET", c.path, nil, c.hdr)
		if resp.StatusCode != c.want {
			t.Errorf("%s: got %d want %d (%s)", c.name, resp.StatusCode, c.want, body)
		}
		if c.want == 404 && strings.HasPrefix(c.path, "/m/") && len(body) != 0 {
			t.Errorf("%s: 404 body must be empty, got %q", c.name, body)
		}
	}
	resp, body := e.do("GET", "/agent/v1/tools", nil, map[string]string{"Authorization": "Bearer " + runTok})
	var tl contract.ToolListResponse
	if err := json.Unmarshal(body, &tl); err != nil || len(tl.Tools) != 13 || resp.StatusCode != 200 {
		t.Fatalf("tool list: %d %v %d", resp.StatusCode, err, len(tl.Tools))
	}
	resp, body = e.do("GET", "/m/"+e.token+"/", nil, nil)
	if !strings.Contains(string(body), "Mind Map") || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("static app: %s", body)
	}
}

func TestBridgeToolCall(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.StartRun(context.Background(), store.NewRun{MapID: e.m.ID, NodeID: e.m.RootID, StartedBy: e.host.ID}); err != nil {
		t.Fatal(err)
	}
	runTok := e.svc.Launcher.(*app.FakeLauncher).Launches[0].RunToken
	auth := map[string]string{"Authorization": "Bearer " + runTok, "X-Mindmap-Contract": "1"}
	resp, body := e.do("POST", "/agent/v1/tools/read", map[string]any{"depth": 2}, auth)
	var res contract.ToolResult
	_ = json.Unmarshal(body, &res)
	if resp.StatusCode != 200 || res.IsError || !strings.Contains(res.Text, "Launch plan") {
		t.Fatalf("read: %d %s", resp.StatusCode, body)
	}
	// Tool errors are still 200 so the model sees them.
	resp, body = e.do("POST", "/agent/v1/tools/add", map[string]any{}, auth)
	_ = json.Unmarshal(body, &res)
	if resp.StatusCode != 200 || !res.IsError || !strings.Contains(res.Text, "nodes") {
		t.Fatalf("add {}: %d %s", resp.StatusCode, body)
	}
}

type wsClient struct {
	t    *testing.T
	conn *websocket.Conn
}

func (e *env) dial(clientID string) *wsClient {
	e.t.Helper()
	u := "ws" + strings.TrimPrefix(e.http.URL, "http") + "/m/" + e.token + "/ws?map_id=" + e.m.ID
	conn, resp, err := websocket.DefaultDialer.Dial(u, http.Header{"Origin": []string{"http://127.0.0.1:38430"}})
	if err != nil {
		e.t.Fatalf("dial: %v (%v)", err, resp)
	}
	e.t.Cleanup(func() { _ = conn.Close() })
	c := &wsClient{t: e.t, conn: conn}
	c.send(contract.MsgHello, &contract.HelloPayload{Contract: 1, ClientID: clientID})
	if m := c.read(); m.Type != contract.MsgWelcome {
		e.t.Fatalf("want welcome, got %s", m.Type)
	}
	if m := c.read(); m.Type != contract.MsgSnapshot {
		e.t.Fatalf("want snapshot, got %s", m.Type)
	}
	return c
}

func (c *wsClient) send(t contract.MsgType, p any) {
	c.t.Helper()
	b, _ := json.Marshal(contract.Message{Type: t, Payload: p})
	if err := c.conn.WriteMessage(websocket.TextMessage, b); err != nil {
		c.t.Fatal(err)
	}
}

// read returns the next frame, skipping participant/activity noise.
func (c *wsClient) read(skip ...contract.MsgType) contract.Message {
	c.t.Helper()
	for {
		_ = c.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, b, err := c.conn.ReadMessage()
		if err != nil {
			c.t.Fatalf("read: %v", err)
		}
		var m contract.Message
		if err := json.Unmarshal(b, &m); err != nil {
			c.t.Fatalf("decode %s: %v", b, err)
		}
		if m.Type == contract.MsgParticipant || m.Type == contract.MsgActivity {
			continue
		}
		skipped := false
		for _, s := range skip {
			if m.Type == s {
				skipped = true
			}
		}
		if !skipped {
			return m
		}
	}
}

func TestWSHubTwoClients(t *testing.T) {
	e := newEnv(t)
	a, b := e.dial("tab-a"), e.dial("tab-b")
	id := store.NewID(contract.PrefixNode)
	op := createOp(e.m.ID, id, e.m.RootID, "From A")
	a.send(contract.MsgSubmitOps, &contract.SubmitPayload{Ops: []contract.Op{op}})
	ack := a.read(contract.MsgPresence)
	if ack.Type != contract.MsgAck || ack.Payload.(*contract.AckPayload).Acks[0].Rev != 2 {
		t.Fatalf("sender got %s %+v", ack.Type, ack.Payload)
	}
	ops := b.read(contract.MsgPresence)
	if ops.Type != contract.MsgOps {
		t.Fatalf("other client got %s", ops.Type)
	}
	if p := ops.Payload.(*contract.OpsPayload); p.Rev != 2 || p.Ops[0].ID != op.ID || p.Ops[0].Actor.ParticipantID != e.host.ID {
		t.Fatalf("ops payload %+v", p)
	}
	// Re-submitting the same op id (reconnect) is acked with the original rev.
	a.send(contract.MsgSubmitOps, &contract.SubmitPayload{Ops: []contract.Op{op}})
	if m := a.read(contract.MsgPresence); m.Type != contract.MsgAck || m.Payload.(*contract.AckPayload).Acks[0].Rev != 2 {
		t.Fatalf("resubmit: %s %+v", m.Type, m.Payload)
	}
	// A cycle is rejected to the sender only.
	mv := contract.Op{ID: store.NewID(contract.PrefixOp), MapID: e.m.ID, Type: contract.OpNodeMove, Payload: &contract.NodeMovePayload{ID: id, ParentID: id, Order: "V"}}
	a.send(contract.MsgSubmitOps, &contract.SubmitPayload{Ops: []contract.Op{mv}})
	if m := a.read(contract.MsgPresence); m.Type != contract.MsgReject || m.Payload.(*contract.RejectPayload).Error.Code != contract.ErrCycle {
		t.Fatalf("reject: %s %+v", m.Type, m.Payload)
	}
	// Order-key collision: B inserts at the same spot and gets a rebase then an ack.
	op2 := createOp(e.m.ID, store.NewID(contract.PrefixNode), e.m.RootID, "From B")
	b.send(contract.MsgSubmitOps, &contract.SubmitPayload{Ops: []contract.Op{op2}})
	if m := b.read(contract.MsgPresence); m.Type != contract.MsgRebase {
		t.Fatalf("want rebase, got %s", m.Type)
	}
	if m := a.read(contract.MsgPresence); m.Type != contract.MsgOps {
		t.Fatalf("a should see b's op, got %s", m.Type)
	}
	// Presence fans out.
	exp := time.Now().Add(10 * time.Second)
	a.send(contract.MsgPresenceUpdate, &contract.PresenceUpdatePayload{Presence: contract.Presence{Cursor: &contract.Point{X: 1, Y: 2},
		Editing: &contract.SoftLock{NodeID: id, Field: contract.LockTitle, ExpiresAt: exp}}})
	if m := b.read(); m.Type != contract.MsgPresence || len(m.Payload.(*contract.PresencePayload).Presence) != 1 {
		t.Fatalf("presence: %s %+v", m.Type, m.Payload)
	}
	// Ping/pong.
	b.send(contract.MsgPing, &contract.PingPayload{Nonce: "n1"})
	if m := b.read(contract.MsgPresence); m.Type != contract.MsgPong || m.Payload.(*contract.PingPayload).Nonce != "n1" {
		t.Fatalf("pong: %s", m.Type)
	}
	// Run updates reach every client.
	if _, err := e.svc.StartRun(context.Background(), store.NewRun{MapID: e.m.ID, NodeID: id, StartedBy: e.host.ID}); err != nil {
		t.Fatal(err)
	}
	if m := b.read(contract.MsgPresence, contract.MsgOps); m.Type != contract.MsgRun {
		t.Fatalf("run broadcast: %s", m.Type)
	}
}

func TestWSHelloCatchUp(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 3; i++ {
		if _, err := e.svc.Submit(e.m.ID, []contract.Op{createOp(e.m.ID, store.NewID("nd"), e.m.RootID, "n")}, app.SubmitOptions{Actor: contract.Actor{ParticipantID: e.host.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	u := "ws" + strings.TrimPrefix(e.http.URL, "http") + "/m/" + e.token + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := &wsClient{t: t, conn: conn}
	c.send(contract.MsgHello, &contract.HelloPayload{Contract: 1, ClientID: "tab", SinceRev: 2})
	c.read() // welcome
	m := c.read()
	if m.Type != contract.MsgOps || len(m.Payload.(*contract.OpsPayload).Ops) != 2 {
		t.Fatalf("catch-up: %s %+v", m.Type, m.Payload)
	}
	// Wrong contract version is fatal.
	conn2, _, _ := websocket.DefaultDialer.Dial(u, nil)
	defer conn2.Close()
	c2 := &wsClient{t: t, conn: conn2}
	c2.send(contract.MsgHello, &contract.HelloPayload{Contract: 9})
	if m := c2.read(); m.Type != contract.MsgError || !m.Payload.(*contract.ErrorPayload).Fatal {
		t.Fatalf("version: %s", m.Type)
	}
}

func TestLongPoll(t *testing.T) {
	e := newEnv(t)
	base := "/m/" + e.token + "/api/maps/" + e.m.ID
	_, body := e.do("GET", base+"/sync", nil, nil)
	var first contract.SyncResponse
	if err := json.Unmarshal(body, &first); err != nil || len(first.Messages) != 2 || first.Messages[0].Type != contract.MsgWelcome ||
		first.Messages[1].Type != contract.MsgSnapshot || first.Cursor == "" {
		t.Fatalf("first sync: %v %s", err, body)
	}
	// An idle poll holds for `wait` and returns empty.
	start := time.Now()
	_, body = e.do("GET", base+"/sync?wait=1&cursor="+first.Cursor, nil, nil)
	var idle contract.SyncResponse
	_ = json.Unmarshal(body, &idle)
	if el := time.Since(start); el < 900*time.Millisecond || len(idle.Messages) != 0 {
		t.Fatalf("idle poll returned after %v with %d messages", el, len(idle.Messages))
	}
	// A held poll wakes as soon as someone else commits.
	got := make(chan contract.SyncResponse, 1)
	go func() {
		_, b := e.do("GET", base+"/sync?wait=15&client_id=poller&cursor="+idle.Cursor, nil, nil)
		var r contract.SyncResponse
		_ = json.Unmarshal(b, &r)
		got <- r
	}()
	time.Sleep(200 * time.Millisecond)
	start = time.Now()
	op := createOp(e.m.ID, store.NewID("nd"), e.m.RootID, "via fallback")
	resp, body := e.do("POST", base+"/ops", contract.SubmitRequest{ClientID: "other-tab", Ops: []contract.Op{op}}, nil)
	var sub contract.SyncResponse
	_ = json.Unmarshal(body, &sub)
	if resp.StatusCode != 200 || len(sub.Messages) != 1 || sub.Messages[0].Type != contract.MsgAck {
		t.Fatalf("submit: %d %s", resp.StatusCode, body)
	}
	select {
	case r := <-got:
		if len(r.Messages) != 1 || r.Messages[0].Type != contract.MsgOps || time.Since(start) > 2*time.Second {
			t.Fatalf("woken poll: %+v after %v", r, time.Since(start))
		}
		// The submitter's own ops are skipped for its client_id.
		_, b := e.do("GET", base+"/sync?wait=0.3&client_id=other-tab&cursor="+idle.Cursor, nil, nil)
		var own contract.SyncResponse
		_ = json.Unmarshal(b, &own)
		if len(own.Messages) != 0 {
			t.Fatalf("own ops echoed: %s", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("held poll did not wake")
	}
	// A cursor from before a restart (unknown seq) catches up from the op log.
	_, body = e.do("GET", base+"/sync?cursor=c1.999", nil, nil)
	var cu contract.SyncResponse
	_ = json.Unmarshal(body, &cu)
	if len(cu.Messages) != 1 || cu.Messages[0].Type != contract.MsgOps {
		t.Fatalf("catch-up after restart: %s", body)
	}
	// wait is clamped to 20 s.
	if q := ParseSyncQuery(func(k string) string { return map[string]string{"wait": "300"}[k] }); q.Wait != contract.LongPollMaxHold {
		t.Fatalf("wait not clamped: %v", q.Wait)
	}
}

func TestPortPersistsAcrossRestarts(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := hub.New()
	defer h.Close()
	svc := app.New(st, h)
	d, _ := tools.NewDispatcher(svc)
	s1 := New(svc, d)
	p1, err := s1.Start()
	if err != nil {
		t.Fatal(err)
	}
	_ = s1.Close()
	s2 := New(svc, d)
	p2, err := s2.Start()
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Fatalf("port changed across restart: %d -> %d", p1, p2)
	}
	// Taken port: a new one is picked and saved.
	s3 := New(svc, d)
	p3, err := s3.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	defer s2.Close()
	if p3 == p2 {
		t.Fatal("reused a taken port")
	}
	if v, _, _ := st.GetKV(PortKey); v == "" {
		t.Fatal("port not saved")
	}
}
