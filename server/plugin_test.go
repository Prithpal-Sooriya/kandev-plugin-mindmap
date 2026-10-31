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

func TestMindmapPingLogsAndPongs(t *testing.T) {
	p := newTestPlugin(t)
	res, err := p.InvokeAgentTool(context.Background(), &pluginsdk.AgentToolRequest{
		Name:      "mindmap_ping",
		Arguments: map[string]any{"msg": "hello", "blob": strings.Repeat("x", 500)},
		Context:   pluginsdk.AgentToolContext{TaskID: "t1", SessionID: "s1", Surface: "kanban-task"},
	})
	if err != nil || res.Text != "pong" || res.IsError {
		t.Fatalf("unexpected result %+v err=%v", res, err)
	}
	b, err := os.ReadFile(filepath.Join(p.dataDir, "ping.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"task_id":"t1"`, `"session_id":"s1"`, `"msg":"hello"`, `string len=500`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("log missing %s: %s", want, b)
		}
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
