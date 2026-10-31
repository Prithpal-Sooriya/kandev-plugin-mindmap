package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kandev/kandev/pkg/pluginsdk"
)

func newTestPlugin(t *testing.T) *mindmapPlugin {
	t.Helper()
	t.Setenv("KANDEV_PLUGIN_DATA_DIR", t.TempDir())
	return newPlugin()
}

func TestAgentToolsRouteToStubDispatcher(t *testing.T) {
	p := newTestPlugin(t)
	res, err := p.InvokeAgentTool(context.Background(), &pluginsdk.AgentToolRequest{
		Name:      "context",
		Arguments: map[string]any{},
		Context:   pluginsdk.AgentToolContext{TaskID: "t1", SessionID: "s1", Surface: "kanban-task"},
	})
	if err != nil || !res.IsError || !strings.HasPrefix(res.Text, "error not_implemented:") {
		t.Fatalf("unexpected result %+v err=%v", res, err)
	}
	res, _ = p.InvokeAgentTool(context.Background(), &pluginsdk.AgentToolRequest{Name: "add", Arguments: map[string]any{}})
	if !res.IsError || !strings.Contains(res.Text, "bad_request") {
		t.Fatalf("missing args should be bad_request, got %+v", res)
	}
}

func TestOnEventRecords(t *testing.T) {
	p := newTestPlugin(t)
	err := p.OnEvent(context.Background(), &pluginsdk.Event{
		EventID: "e1", EventType: "agent.completed", Payload: map[string]any{"task_id": "t1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(p.dataDir, "events.log"))
	if !strings.Contains(string(b), `"event_type":"agent.completed"`) {
		t.Fatalf("event not logged: %s", b)
	}
}

func TestProbeServerWebSocketEcho(t *testing.T) {
	p := newTestPlugin(t)
	srv, err := p.ensureServer()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.http.Close() })
	c, _, err := websocket.DefaultDialer.Dial("ws://"+srv.addr+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := c.ReadMessage(); err != nil { // hello frame
		t.Fatal(err)
	}
	_ = c.WriteMessage(websocket.TextMessage, []byte("hi"))
	_, msg, err := c.ReadMessage()
	if err != nil || string(msg) != "echo:hi" {
		t.Fatalf("got %q err=%v", msg, err)
	}
}

func TestSQLiteProbeWorksWithoutCGO(t *testing.T) {
	out := sqliteProbe(t.TempDir())
	if out["error"] != nil || out["version"] == nil {
		t.Fatalf("sqlite probe failed: %v", out)
	}
}
