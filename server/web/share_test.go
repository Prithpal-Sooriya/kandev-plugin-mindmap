package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"kandev-plugin-mindmap/server/app"
	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/share"
	"kandev-plugin-mindmap/server/store"
)

func (e *env) useTunnel(t *testing.T, mgr *share.Manager) {
	t.Helper()
	links, err := share.NewLinks(e.st, mgr)
	if err != nil {
		t.Fatal(err)
	}
	links.Port = e.svc.Port
	e.srv.SetShare(links, nil)
	t.Cleanup(mgr.Stop)
}

func fakeTunnel(t *testing.T) *share.Manager {
	t.Helper()
	dir := t.TempDir()
	body := []byte("#!/bin/sh\necho \"https://${MM_HOST:-demo.trycloudflare.com}\"\nwhile true; do sleep 1; done\n")
	if err := os.WriteFile(filepath.Join(dir, "cloudflared"), body, 0o755); err != nil {
		t.Fatal(err)
	}
	return &share.Manager{
		SkipPath: true, BrewDirs: []string{dir}, URLWait: 2 * time.Second,
		ExtraEnv: []string{"MM_HOST=demo.trycloudflare.com"},
	}
}

func (e *env) shareMap() contract.ShareLink {
	e.t.Helper()
	resp, body := e.do("POST", "/m/"+e.token+"/api/maps/"+e.m.ID+"/share", map[string]any{}, nil)
	if resp.StatusCode != 200 {
		e.t.Fatalf("share: %d %s", resp.StatusCode, body)
	}
	var link contract.ShareLink
	if err := json.Unmarshal(body, &link); err != nil {
		e.t.Fatal(err)
	}
	return link
}

func errCode(t *testing.T, body []byte) contract.ErrorCode {
	t.Helper()
	var env contract.ErrorBody
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("error body %s: %v", body, err)
	}
	return env.Error.Code
}

func TestShareNotInstalled(t *testing.T) {
	e := newEnv(t)
	e.useTunnel(t, &share.Manager{SkipPath: true, BrewDirs: []string{}, URLWait: time.Second})
	resp, body := e.do("POST", "/m/"+e.token+"/api/maps/"+e.m.ID+"/share", map[string]any{}, nil)
	if resp.StatusCode != 503 || errCode(t, body) != contract.ErrNotInstalled || !strings.Contains(string(body), "brew install cloudflared") {
		t.Fatalf("not installed: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do("GET", "/m/"+e.token+"/api/maps/"+e.m.ID+"/share", nil, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("failed share must not stick: %d %s", resp.StatusCode, body)
	}
}

func TestShareLifecycle(t *testing.T) {
	e := newEnv(t)
	mgr := fakeTunnel(t)
	e.useTunnel(t, mgr)
	link := e.shareMap()
	if link.Status != contract.ShareActive || !strings.HasPrefix(link.URL, "https://demo.trycloudflare.com/m/mms1.") || !strings.HasSuffix(link.URL, "/") {
		t.Fatalf("link %+v", link)
	}
	raw := strings.Trim(strings.TrimPrefix(link.URL, "https://demo.trycloudflare.com/m/"), "/")
	resp, body := e.do("GET", "/m/"+e.token+"/api/maps/"+e.m.ID+"/share", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), raw) {
		t.Fatalf("status: %d %s", resp.StatusCode, body)
	}
	if mgr.PID() <= 0 || !mgr.Healthy() {
		t.Fatal("tunnel not up")
	}

	other, _, err := e.st.CreateMap(contract.CreateMapRequest{WorkspaceID: "ws-1", Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	resp, body = e.do("GET", "/m/"+raw+"/api/maps/"+other.ID+"/snapshot", nil, nil)
	if resp.StatusCode != 404 || len(body) != 0 {
		t.Fatalf("other map: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do("POST", "/m/"+raw+"/api/maps/"+e.m.ID+"/share", map[string]any{}, nil)
	if resp.StatusCode != 403 || errCode(t, body) != contract.ErrForbidden {
		t.Fatalf("guest share: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do("POST", "/m/"+raw+"/api/kill", map[string]any{}, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("guest kill: %d %s", resp.StatusCode, body)
	}

	resp, body = e.do("POST", "/m/"+e.token+"/api/maps/"+e.m.ID+"/share/rotate", map[string]any{}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("rotate: %d %s", resp.StatusCode, body)
	}
	var rotated contract.ShareLink
	_ = json.Unmarshal(body, &rotated)
	if rotated.URL == link.URL || !strings.Contains(rotated.URL, "/m/mms1.") {
		t.Fatalf("rotated %+v", rotated)
	}
	resp, body = e.do("GET", "/m/"+raw+"/api/maps/"+e.m.ID+"/snapshot", nil, nil)
	if resp.StatusCode != 404 || len(body) != 0 {
		t.Fatalf("old token: %d %q", resp.StatusCode, body)
	}
	if mgr.PID() <= 0 {
		t.Fatal("rotate stopped the tunnel")
	}

	resp, _ = e.do("DELETE", "/m/"+e.token+"/api/maps/"+e.m.ID+"/share", nil, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("stop %d", resp.StatusCode)
	}
	if mgr.PID() != 0 {
		t.Fatalf("tunnel still up after the last share stopped: %d", mgr.PID())
	}
	raw2 := strings.Trim(strings.TrimPrefix(rotated.URL, "https://demo.trycloudflare.com/m/"), "/")
	resp, body = e.do("GET", "/m/"+raw2+"/", nil, nil)
	if resp.StatusCode != 404 || len(body) != 0 {
		t.Fatalf("stopped token: %d %q", resp.StatusCode, body)
	}
}

func TestTwoMapsKeepTheTunnel(t *testing.T) {
	e := newEnv(t)
	mgr := fakeTunnel(t)
	e.useTunnel(t, mgr)
	e.shareMap()
	other, _, err := e.st.CreateMap(contract.CreateMapRequest{WorkspaceID: "ws-1", Title: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := e.st.IssueToken(contract.TokenLocal, 0, "", "", e.host.ID, "", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	resp, body := e.do("POST", "/m/"+tok+"/api/maps/"+other.ID+"/share", map[string]any{}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("second share: %d %s", resp.StatusCode, body)
	}
	resp, _ = e.do("DELETE", "/m/"+e.token+"/api/maps/"+e.m.ID+"/share", nil, nil)
	if resp.StatusCode != 204 || mgr.PID() == 0 {
		t.Fatalf("stopping one map stopped the tunnel pid=%d status=%d", mgr.PID(), resp.StatusCode)
	}
	resp, _ = e.do("DELETE", "/m/"+tok+"/api/maps/"+other.ID+"/share", nil, nil)
	if resp.StatusCode != 204 || mgr.PID() != 0 {
		t.Fatalf("tunnel after last stop: status %d pid %d", resp.StatusCode, mgr.PID())
	}
}

func TestGuestCookieSession(t *testing.T) {
	e := newEnv(t)
	e.useTunnel(t, fakeTunnel(t))
	link := e.shareMap()
	raw := strings.Trim(strings.TrimPrefix(link.URL, "https://demo.trycloudflare.com/m/"), "/")

	resp, body := e.do("POST", "/m/"+raw+"/api/join", map[string]any{"name": "Ada", "color": "#3b82f6"}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("join: %d %s", resp.StatusCode, body)
	}
	var guest contract.Participant
	if err := json.Unmarshal(body, &guest); err != nil {
		t.Fatal(err)
	}
	if guest.IsHost || guest.ID == e.host.ID || guest.Name != "Ada" {
		t.Fatalf("guest %+v", guest)
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies %d", len(cookies))
	}
	c := cookies[0]
	if c.Name != contract.GuestCookieName || !c.HttpOnly || c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/m/"+raw+"/" {
		t.Fatalf("cookie %+v", c)
	}
	if !strings.Contains(resp.Header.Get("Set-Cookie"), "SameSite=Lax") {
		t.Fatalf("set-cookie %s", resp.Header.Get("Set-Cookie"))
	}

	resp, body = e.do("POST", "/m/"+raw+"/api/join", map[string]any{"name": "Ada", "color": "#3b82f6"}, map[string]string{
		"Cf-Connecting-Ip": "203.0.113.9", "X-Forwarded-Proto": "https",
		"Cookie": contract.GuestCookieName + "=" + c.Value,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("rejoin: %d %s", resp.StatusCode, body)
	}
	secure := resp.Cookies()
	if len(secure) != 1 || !secure[0].Secure || secure[0].Value != c.Value {
		t.Fatalf("secure cookie %+v", secure)
	}

	hdr := map[string]string{"Cookie": contract.GuestCookieName + "=" + c.Value}
	id := store.NewID(contract.PrefixNode)
	op := createOp(e.m.ID, id, e.m.RootID, "Guest idea")
	resp, body = e.do("POST", "/m/"+raw+"/api/maps/"+e.m.ID+"/ops", contract.SubmitRequest{ClientID: "guest", Ops: []contract.Op{op}}, hdr)
	if resp.StatusCode != 200 {
		t.Fatalf("guest op: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do("GET", "/m/"+e.token+"/api/maps/"+e.m.ID+"/snapshot", nil, nil)
	var snap contract.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, n := range snap.Nodes {
		if n.ID == id {
			found = n.Author.ParticipantID == guest.ID
		}
	}
	if !found {
		t.Fatal("op was not attributed to the guest")
	}

	resp, body = e.do("POST", "/m/"+raw+"/api/maps/"+e.m.ID+"/ops", contract.SubmitRequest{ClientID: "guest", Ops: []contract.Op{op}}, nil)
	if resp.StatusCode != 400 || errCode(t, body) != contract.ErrBadRequest {
		t.Fatalf("no cookie: %d %s", resp.StatusCode, body)
	}

	first := e.dialShare(raw, c.Value, "tab-1", e.host.ID)
	if first != guest.ID {
		t.Fatalf("cookie hello resumed %s, want %s (host %s)", first, guest.ID, e.host.ID)
	}
	again := e.dialShare(raw, c.Value, "tab-2", "pt_imposter")
	if again != guest.ID {
		t.Fatalf("second hello %s", again)
	}
	fresh := e.dialShare(raw, "", "tab-3", e.host.ID)
	if fresh == guest.ID || fresh == e.host.ID {
		t.Fatalf("cookie-less hello reused %s", fresh)
	}
}

func (e *env) dialShare(raw, cookie, clientID, resume string) string {
	e.t.Helper()
	u := "ws" + strings.TrimPrefix(e.http.URL, "http") + "/m/" + raw + "/ws?map_id=" + e.m.ID
	hdr := http.Header{}
	if cookie != "" {
		hdr.Set("Cookie", contract.GuestCookieName+"="+cookie)
	}
	conn, resp, err := websocket.DefaultDialer.Dial(u, hdr)
	if err != nil {
		e.t.Fatalf("dial: %v (%v)", err, resp)
	}
	e.t.Cleanup(func() { _ = conn.Close() })
	c := &wsClient{t: e.t, conn: conn}
	c.send(contract.MsgHello, &contract.HelloPayload{Contract: 1, ClientID: clientID, Name: "Ada", Color: "#3b82f6", ParticipantID: resume})
	m := c.read()
	wp, ok := m.Payload.(*contract.WelcomePayload)
	if !ok || m.Type != contract.MsgWelcome {
		e.t.Fatalf("welcome: %s", m.Type)
	}
	if s := c.read(); s.Type != contract.MsgSnapshot {
		e.t.Fatalf("snapshot: %s", s.Type)
	}
	return wp.Participant.ID
}

func TestTunnelCannotReachHostRoutes(t *testing.T) {
	e := newEnv(t)
	run, err := e.svc.StartRun(context.Background(), app.RunSpec{NewRun: store.NewRun{MapID: e.m.ID, NodeID: e.m.RootID, StartedBy: e.host.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_ = run
	runTok := e.svc.Launcher.(*app.FakeLauncher).Launches[0].RunToken
	cases := []struct {
		name, path, host string
		hdr              map[string]string
	}{
		{"cf", "/m/" + e.token + "/api/maps", "", map[string]string{"Cf-Connecting-Ip": "203.0.113.8"}},
		{"xff", "/m/" + e.token + "/api/maps", "", map[string]string{"X-Forwarded-For": "203.0.113.8"}},
		{"tailscale", "/m/" + e.token + "/api/maps", "", map[string]string{"Tailscale-User-Login": "guest@example.com"}},
		{"host trycloudflare", "/m/" + e.token + "/api/maps", "demo.trycloudflare.com", nil},
		{"host ts.net", "/m/" + e.token + "/", "box.tailnet.ts.net", nil},
		{"bridge host", "/agent/v1/tools", "demo.trycloudflare.com", map[string]string{"Authorization": "Bearer " + runTok}},
		{"bridge tailscale", "/agent/v1/tools", "", map[string]string{"Authorization": "Bearer " + runTok, "Tailscale-User-Login": "guest"}},
		{"kandev root", "/", "demo.trycloudflare.com", nil},
		{"kandev api", "/api/plugins", "demo.trycloudflare.com", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, body := e.doHost(c.path, c.host, c.hdr)
			if resp.StatusCode != 404 {
				t.Fatalf("got %d %s", resp.StatusCode, body)
			}
			if strings.HasPrefix(c.path, "/m/") && len(body) != 0 {
				t.Fatalf("body %q", body)
			}
		})
	}
}

func (e *env) doHost(path, host string, hdr map[string]string) (*http.Response, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.http.URL+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
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

func TestRateLimits(t *testing.T) {
	e := newEnv(t)
	e.srv.SetShare(nil, share.NewLimiter(share.LimitConfig{
		OpsRate: 0.001, OpsBurst: 1,
		APIRate: 0.001, APIBurst: 1,
		RunRate: 100, RunBurst: 10,
		AssetRate: 100, AssetBurst: 10,
	}))
	path := "/m/" + e.token + "/api/maps/" + e.m.ID + "/snapshot"
	resp, _ := e.do("GET", path, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("first %d", resp.StatusCode)
	}
	resp, body := e.do("GET", path, nil, nil)
	if resp.StatusCode != 429 || errCode(t, body) != contract.ErrRateLimited || !strings.Contains(string(body), "retry_ms") {
		t.Fatalf("limited: %d %s", resp.StatusCode, body)
	}

	e.srv.SetShare(nil, share.NewLimiter(share.LimitConfig{
		OpsRate: 0.001, OpsBurst: 1,
		APIRate: 100, APIBurst: 100,
		RunRate: 100, RunBurst: 10,
		AssetRate: 100, AssetBurst: 10,
	}))
	op := createOp(e.m.ID, store.NewID(contract.PrefixNode), e.m.RootID, "One")
	resp, body = e.do("POST", "/m/"+e.token+"/api/maps/"+e.m.ID+"/ops", contract.SubmitRequest{ClientID: "h", Ops: []contract.Op{op}}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("op: %d %s", resp.StatusCode, body)
	}
	op2 := createOp(e.m.ID, store.NewID(contract.PrefixNode), e.m.RootID, "Two")
	resp, body = e.do("POST", "/m/"+e.token+"/api/maps/"+e.m.ID+"/ops", contract.SubmitRequest{ClientID: "h", Ops: []contract.Op{op2}}, nil)
	if resp.StatusCode != 429 || errCode(t, body) != contract.ErrRateLimited {
		t.Fatalf("ops limited: %d %s", resp.StatusCode, body)
	}
}

func TestRunCapsApprovalAndKill(t *testing.T) {
	e := newEnv(t)
	mgr := fakeTunnel(t)
	e.useTunnel(t, mgr)
	e.shareMap()

	settings := contract.MapSettings{MaxConcurrentRun: 1}
	op := contract.Op{ID: store.NewID(contract.PrefixOp), MapID: e.m.ID, Type: contract.OpMapUpdate, Payload: &contract.MapUpdatePayload{Settings: &settings}}
	resp, body := e.do("POST", "/m/"+e.token+"/api/maps/"+e.m.ID+"/ops", contract.SubmitRequest{ClientID: "h", Ops: []contract.Op{op}}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("settings op: %d %s", resp.StatusCode, body)
	}
	if err := e.svc.CheckRunCaps(e.m.ID, nil, 1); err != nil {
		t.Fatal(err)
	}
	resp, body = e.do("POST", "/m/"+e.token+"/api/maps/"+e.m.ID+"/runs", contract.StartRunRequest{NodeID: e.m.RootID, Template: contract.TemplateResearch}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("run: %d %s", resp.StatusCode, body)
	}
	var started contract.StartRunsResponse
	if err := json.Unmarshal(body, &started); err != nil || len(started.Runs) != 1 {
		t.Fatalf("runs: %s", body)
	}
	if err := e.svc.CheckRunCaps(e.m.ID, nil, 1); err == nil || contract.AsError(err).Code != contract.ErrBudgetExceeded {
		t.Fatalf("cap: %v", err)
	}
	resp, body = e.do("POST", "/m/"+e.token+"/api/maps/"+e.m.ID+"/runs", contract.StartRunRequest{NodeID: e.m.RootID, Template: contract.TemplateResearch}, nil)
	if resp.StatusCode != 403 || errCode(t, body) != contract.ErrBudgetExceeded {
		t.Fatalf("second run: %d %s", resp.StatusCode, body)
	}

	on := contract.MapSettings{MaxConcurrentRun: 1, RequireGuestRunApproval: true}
	op = contract.Op{ID: store.NewID(contract.PrefixOp), MapID: e.m.ID, Type: contract.OpMapUpdate, Payload: &contract.MapUpdatePayload{Settings: &on}}
	resp, body = e.do("POST", "/m/"+e.token+"/api/maps/"+e.m.ID+"/ops", contract.SubmitRequest{ClientID: "h", Ops: []contract.Op{op}}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("approval op: %d %s", resp.StatusCode, body)
	}
	link, _, err := e.srv.links.Status(e.m.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.Trim(strings.TrimPrefix(link.URL, "https://demo.trycloudflare.com/m/"), "/")
	resp, body = e.do("POST", "/m/"+raw+"/api/join", map[string]any{"name": "Bea", "color": "#22c55e"}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("join: %d %s", resp.StatusCode, body)
	}
	ck := resp.Cookies()[0]
	guestHdr := map[string]string{"Cookie": ck.Name + "=" + ck.Value}
	// A held guest run is queued even at the cap; it does not count until approved.
	resp, body = e.do("POST", "/m/"+raw+"/api/maps/"+e.m.ID+"/runs", contract.StartRunRequest{NodeID: e.m.RootID, Template: contract.TemplateResearch}, guestHdr)
	var held contract.StartRunsResponse
	if resp.StatusCode != 200 || json.Unmarshal(body, &held) != nil || len(held.Runs) != 1 {
		t.Fatalf("guest run: %d %s", resp.StatusCode, body)
	}
	if r := held.Runs[0]; r.Status != contract.RunWaiting || r.WaitingReason != contract.WaitingApproval || r.TaskID != "" {
		t.Fatalf("guest run not held: %+v", r)
	}
	approve := "/api/runs/" + held.Runs[0].ID + "/approve"
	resp, body = e.do("POST", "/m/"+raw+approve, map[string]any{}, guestHdr)
	if resp.StatusCode != 403 {
		t.Fatalf("guest approved: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do("POST", "/m/"+e.token+approve, map[string]any{}, nil)
	if resp.StatusCode != 403 || errCode(t, body) != contract.ErrBudgetExceeded {
		t.Fatalf("approve over cap: %d %s", resp.StatusCode, body)
	}
	if _, err := e.svc.StopRun(context.Background(), started.Runs[0].ID, e.host.ID, "make room"); err != nil {
		t.Fatal(err)
	}
	resp, body = e.do("POST", "/m/"+e.token+approve, map[string]any{}, nil)
	var approved contract.StartRunsResponse
	if resp.StatusCode != 200 || json.Unmarshal(body, &approved) != nil || len(approved.Runs) != 1 || approved.Runs[0].TaskID == "" {
		t.Fatalf("approve: %d %s", resp.StatusCode, body)
	}
	launches := e.svc.Launcher.(*app.FakeLauncher).Requests()
	guestTok := launches[len(launches)-1].RunToken
	hold, err := e.svc.GuestRunNeedsApproval(e.m.ID, e.host.ID)
	if err != nil || hold {
		t.Fatalf("host held: %v %v", hold, err)
	}

	resp, body = e.do("GET", "/m/"+e.token+"/api/maps/"+e.m.ID+"/audit", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "run.started") || !strings.Contains(string(body), e.host.ID) {
		t.Fatalf("audit: %d %s", resp.StatusCode, body)
	}

	msgs := &recordMessenger{}
	e.svc.Messenger = msgs
	resp, _ = e.do("POST", "/m/"+e.token+"/api/kill", map[string]any{}, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("kill %d", resp.StatusCode)
	}
	if mgr.PID() != 0 {
		t.Fatal("kill left the tunnel up")
	}
	got, err := e.st.GetRun(approved.Runs[0].ID)
	if err != nil || got.Status != contract.RunStopped || !got.StopRequested {
		t.Fatalf("run after kill: %+v %v", got, err)
	}
	if sent := msgs.sent(); len(sent) != 1 || sent[0] != got.TaskID {
		t.Fatalf("kill messaged %v, want the live agent %s", sent, got.TaskID)
	}
	if _, err := e.st.LookupToken(guestTok); err == nil {
		t.Fatal("run token survived the kill switch")
	}
	resp, body = e.do("GET", "/m/"+raw+"/api/maps/"+e.m.ID+"/snapshot", nil, nil)
	if resp.StatusCode != 404 || len(body) != 0 {
		t.Fatalf("revoked share: %d %q", resp.StatusCode, body)
	}
	if _, err := e.st.LookupToken(ck.Value); err == nil {
		t.Fatal("guest session still valid")
	}
}

func TestAssetLimits(t *testing.T) {
	e := newEnv(t)
	status, body := e.upload(pngBytes(32), "image/png", "dot.png")
	if status != 200 {
		t.Fatalf("png: %d %s", status, body)
	}
	status, body = e.upload([]byte("not an image"), "image/png", "nope.png")
	if status != 415 || errCode(t, body) != contract.ErrUnsupportedMedia {
		t.Fatalf("mime: %d %s", status, body)
	}
	status, body = e.upload(pngBytes(contract.MaxAssetBytes+1), "image/png", "big.png")
	if status != 413 || errCode(t, body) != contract.ErrTooLarge {
		t.Fatalf("size: %d %s", status, body)
	}
}

func TestShareSettingsRoundTrip(t *testing.T) {
	e := newEnv(t)
	in := contract.ShareSettings{Provider: contract.ShareTailscale, MaxConcurrentRuns: 3, MaxNodes: 20, RequireGuestRunApproval: true}
	resp, body := e.do("PUT", "/m/"+e.token+"/api/settings/share", in, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("put: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do("GET", "/m/"+e.token+"/api/settings/share", nil, nil)
	var got contract.ShareSettings
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Provider != contract.ShareTailscale || got.MaxConcurrentRuns != 3 || got.MaxNodes != 20 || !got.RequireGuestRunApproval {
		t.Fatalf("got %+v", got)
	}
	resp, body = e.do("PUT", "/m/"+e.token+"/api/settings/share", contract.ShareSettings{Provider: "nope"}, nil)
	if resp.StatusCode != 400 {
		t.Fatalf("bad provider: %d %s", resp.StatusCode, body)
	}
}

type recordMessenger struct {
	mu    sync.Mutex
	tasks []string
}

func (m *recordMessenger) Send(_ context.Context, taskID, _, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks = append(m.tasks, taskID)
	return nil
}

func (m *recordMessenger) sent() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.tasks...)
}

func shareRaw(link contract.ShareLink) string {
	u := strings.TrimRight(link.URL, "/")
	return u[strings.LastIndex(u, "/")+1:]
}

func TestRotateClosesGuestSocket(t *testing.T) {
	e := newEnv(t)
	e.useTunnel(t, fakeTunnel(t))
	old := shareRaw(e.shareMap())
	host := e.dial("host-stays")
	u := "ws" + strings.TrimPrefix(e.http.URL, "http") + "/m/" + old + "/ws?map_id=" + e.m.ID
	conn, resp, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("dial: %v (%v)", err, resp)
	}
	t.Cleanup(func() { _ = conn.Close() })
	guest := &wsClient{t: t, conn: conn}
	guest.send(contract.MsgHello, &contract.HelloPayload{Contract: 1, ClientID: "guest-old", Name: "Ada", Color: "#3b82f6"})
	if m := guest.read(); m.Type != contract.MsgWelcome {
		t.Fatalf("welcome %s", m.Type)
	}
	if m := guest.read(); m.Type != contract.MsgSnapshot {
		t.Fatalf("snapshot %s", m.Type)
	}

	respHTTP, body := e.do("POST", "/m/"+e.token+"/api/maps/"+e.m.ID+"/share/rotate", map[string]any{}, nil)
	if respHTTP.StatusCode != 200 {
		t.Fatalf("rotate: %d %s", respHTTP.StatusCode, body)
	}
	var next contract.ShareLink
	if err := json.Unmarshal(body, &next); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	closed := false
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		if _, _, rerr := conn.ReadMessage(); rerr != nil {
			closed = true
			break
		}
	}
	if !closed {
		t.Fatal("old socket stayed open after rotate")
	}
	_ = conn.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
	if werr := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping","payload":{"nonce":"after"}}`)); werr == nil {
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		if _, _, rerr := conn.ReadMessage(); rerr == nil {
			t.Fatal("old socket accepted a frame after rotate")
		}
	}
	got, gotBody := e.do("GET", "/m/"+old+"/api/maps/"+e.m.ID+"/snapshot", nil, nil)
	if got.StatusCode != 404 || len(gotBody) != 0 {
		t.Fatalf("old token: %d %q", got.StatusCode, gotBody)
	}
	if id := e.dialShare(shareRaw(next), "", "guest-new", ""); id == "" || id == e.host.ID {
		t.Fatalf("new token guest %s", id)
	}
	host.send(contract.MsgPing, &contract.PingPayload{Nonce: "still-here"})
	if m := host.read(); m.Type != contract.MsgPong {
		t.Fatalf("host socket: %s", m.Type)
	}
}

func TestRejoinDoesNotResetRunLimit(t *testing.T) {
	e := newEnv(t)
	e.useTunnel(t, fakeTunnel(t))
	raw := shareRaw(e.shareMap())
	e.srv.SetShare(nil, share.NewLimiter(share.LimitConfig{
		OpsRate: 100, OpsBurst: 100,
		APIRate: 100, APIBurst: 100,
		RunRate: 0.001, RunBurst: 2,
		AssetRate: 100, AssetBurst: 10,
		JoinRate: 100, JoinBurst: 20,
	}))
	start := func(cookie string) (int, []byte) {
		t.Helper()
		resp, body := e.do("POST", "/m/"+raw+"/api/maps/"+e.m.ID+"/runs", contract.StartRunRequest{
			NodeID: e.m.RootID, Template: contract.TemplateResearch,
		}, map[string]string{"Cookie": cookie})
		return resp.StatusCode, body
	}
	var cookies []string
	for i := 0; i < 3; i++ {
		resp, body := e.do("POST", "/m/"+raw+"/api/join", map[string]any{"name": "Ada", "color": "#3b82f6"}, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("join %d: %d %s", i, resp.StatusCode, body)
		}
		if len(resp.Cookies()) == 0 {
			t.Fatal("no cookie")
		}
		ck := resp.Cookies()[0]
		cookies = append(cookies, ck.Name+"="+ck.Value)
	}
	for i := 0; i < 2; i++ {
		if status, body := start(cookies[0]); status != 200 {
			t.Fatalf("run %d: %d %s", i, status, body)
		}
	}
	if status, body := start(cookies[0]); status != 429 || errCode(t, body) != contract.ErrRateLimited {
		t.Fatalf("third run: %d %s", status, body)
	}
	for i, c := range cookies[1:] {
		status, body := start(c)
		if status != 429 || errCode(t, body) != contract.ErrRateLimited {
			t.Fatalf("rejoin %d started a run past the cap: %d %s", i, status, body)
		}
	}
}

func TestJoinMintIsCappedPerLink(t *testing.T) {
	e := newEnv(t)
	e.useTunnel(t, fakeTunnel(t))
	raw := shareRaw(e.shareMap())
	e.srv.SetShare(nil, share.NewLimiter(share.LimitConfig{
		OpsRate: 100, OpsBurst: 100,
		APIRate: 100, APIBurst: 100,
		RunRate: 100, RunBurst: 10,
		AssetRate: 100, AssetBurst: 10,
		JoinRate: 0.001, JoinBurst: 2,
	}))
	var cookie string
	for i := 0; i < 2; i++ {
		resp, body := e.do("POST", "/m/"+raw+"/api/join", map[string]any{"name": "Ada", "color": "#3b82f6"}, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("join %d: %d %s", i, resp.StatusCode, body)
		}
		if i == 0 {
			ck := resp.Cookies()[0]
			cookie = ck.Name + "=" + ck.Value
		}
	}
	resp, body := e.do("POST", "/m/"+raw+"/api/join", map[string]any{"name": "Bea", "color": "#22c55e"}, nil)
	if resp.StatusCode != 429 || errCode(t, body) != contract.ErrRateLimited {
		t.Fatalf("third mint: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do("POST", "/m/"+raw+"/api/join", map[string]any{"name": "Ada", "color": "#3b82f6"}, map[string]string{"Cookie": cookie})
	if resp.StatusCode != 200 {
		t.Fatalf("cookie rejoin should not mint: %d %s", resp.StatusCode, body)
	}
}

func TestSharedMapClampsGuestBudget(t *testing.T) {
	e := newEnv(t)
	huge := &contract.Budget{MaxMinutes: 100000, MaxSubagents: 500}
	if err := e.svc.CheckRunCaps(e.m.ID, huge, 5); err != nil {
		t.Fatalf("unshared map should stay unlimited: %v", err)
	}
	e.useTunnel(t, fakeTunnel(t))
	raw := shareRaw(e.shareMap())
	if err := e.svc.CheckRunCaps(e.m.ID, nil, 1); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.CheckRunCaps(e.m.ID, nil, 5); err == nil || contract.AsError(err).Code != contract.ErrBudgetExceeded {
		t.Fatalf("shared default concurrency: %v", err)
	}
	resp, body := e.do("POST", "/m/"+raw+"/api/join", map[string]any{"name": "Ada", "color": "#3b82f6"}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("join: %d %s", resp.StatusCode, body)
	}
	cookie := resp.Cookies()[0].Name + "=" + resp.Cookies()[0].Value
	start := func() contract.Run {
		t.Helper()
		resp, body := e.do("POST", "/m/"+raw+"/api/maps/"+e.m.ID+"/runs", contract.StartRunRequest{
			NodeID: e.m.RootID, ProfileID: "prof-guest", Template: contract.TemplateResearch, Budget: huge,
		}, map[string]string{"Cookie": cookie})
		if resp.StatusCode != 200 {
			t.Fatalf("run: %d %s", resp.StatusCode, body)
		}
		var out contract.StartRunsResponse
		if err := json.Unmarshal(body, &out); err != nil || len(out.Runs) != 1 {
			t.Fatalf("runs: %s", body)
		}
		return out.Runs[0]
	}
	run := start()
	if run.ProfileID != "prof-guest" {
		t.Fatalf("profile %s", run.ProfileID)
	}
	if run.Budget.MaxMinutes != store.DefaultBudget.MaxMinutes || run.Budget.MaxSubagents != store.DefaultBudget.MaxSubagents {
		t.Fatalf("budget not clamped: %+v", run.Budget)
	}
	resp, body = e.do("PUT", "/m/"+e.token+"/api/settings/share", contract.ShareSettings{
		Provider: contract.ShareCloudflared, MaxMinutes: 90, MaxSubagents: 8,
	}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("settings: %d %s", resp.StatusCode, body)
	}
	run = start()
	if run.Budget.MaxMinutes != 90 || run.Budget.MaxSubagents != 8 || run.ProfileID != "prof-guest" {
		t.Fatalf("host cap not applied: %+v profile %s", run.Budget, run.ProfileID)
	}
}

func TestUnauthenticatedUploadWritesNothing(t *testing.T) {
	e := newEnv(t)
	e.useTunnel(t, fakeTunnel(t))
	raw := shareRaw(e.shareMap())
	before := blobCount(t, e.st.DataDir())
	status, body := e.uploadAs(raw, "", pngBytes(32), "image/png", "dot.png")
	if status != 400 || errCode(t, body) != contract.ErrBadRequest {
		t.Fatalf("guest upload: %d %s", status, body)
	}
	if n := blobCount(t, e.st.DataDir()); n != before {
		t.Fatalf("rejected upload wrote %d blobs, had %d", n, before)
	}
	resp, body := e.do("POST", "/m/"+raw+"/api/join", map[string]any{"name": "Ada", "color": "#3b82f6"}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("join: %d %s", resp.StatusCode, body)
	}
	cookie := resp.Cookies()[0].Name + "=" + resp.Cookies()[0].Value
	status, body = e.uploadAs(raw, cookie, pngBytes(32), "image/png", "dot.png")
	if status != 200 {
		t.Fatalf("joined upload: %d %s", status, body)
	}
	if n := blobCount(t, e.st.DataDir()); n != before+1 {
		t.Fatalf("joined upload blobs %d, want %d", n, before+1)
	}
}

func blobCount(t *testing.T, dir string) int {
	t.Helper()
	root := filepath.Join(dir, "assets")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return 0
	}
	n := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) upload(data []byte, mime, name string) (int, []byte) {
	return e.uploadAs(e.token, "", data, mime, name)
}

func (e *env) uploadAs(token, cookie string, data []byte, mime, name string) (int, []byte) {
	e.t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
	h.Set("Content-Type", mime)
	part, err := w.CreatePart(h)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		e.t.Fatal(err)
	}
	if err := w.WriteField("node_id", e.m.RootID); err != nil {
		e.t.Fatal(err)
	}
	_ = w.Close()
	req, err := http.NewRequest(http.MethodPost, e.http.URL+"/m/"+token+"/api/maps/"+e.m.ID+"/assets", &buf)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func pngBytes(n int) []byte {
	b := make([]byte, n)
	copy(b, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	return b
}
