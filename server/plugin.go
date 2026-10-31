package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"

	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/tools"
)

// mindmapPlugin is the Phase 0 spike plugin plus the stubbed contract tools: a handful of
// authenticated debug webhooks, and a loopback HTTP+WS probe server.
type mindmapPlugin struct {
	pluginsdk.UnimplementedPlugin

	dataDir string
	tools   *tools.Dispatcher

	logMu sync.Mutex

	srvOnce sync.Once
	srv     *probeServer
	srvErr  error
}

var (
	_ pluginsdk.Plugin          = (*mindmapPlugin)(nil)
	_ pluginsdk.AgentToolPlugin = (*mindmapPlugin)(nil)
)

func newPlugin() *mindmapPlugin {
	dir := os.Getenv("KANDEV_PLUGIN_DATA_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "kandev-plugin-mindmap")
	}
	_ = os.MkdirAll(dir, 0o755)
	return &mindmapPlugin{dataDir: dir, tools: tools.NewStub()}
}

// appendLog writes one JSON line to <data dir>/<name>.log.
func (p *mindmapPlugin) appendLog(name string, entry map[string]any) error {
	entry["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	p.logMu.Lock()
	defer p.logMu.Unlock()
	f, err := os.OpenFile(filepath.Join(p.dataDir, name+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// InvokeAgentTool implements pluginsdk.AgentToolPlugin by routing the call to
// the shared tool dispatcher (stubbed until Phase 1a).
func (p *mindmapPlugin) InvokeAgentTool(ctx context.Context, req *pluginsdk.AgentToolRequest) (*pluginsdk.AgentToolResult, error) {
	res := p.tools.InvokeMap(ctx, tools.Caller{
		Transport:    contract.TransportMCP,
		InvocationID: req.InvocationID,
		TaskID:       req.Context.TaskID,
		SessionID:    req.Context.SessionID,
		WorkspaceID:  req.Context.WorkspaceID,
	}, req.Name, req.Arguments)
	return &pluginsdk.AgentToolResult{Text: res.Text, StructuredContent: res.Structured, IsError: res.IsError}, nil
}

// OnEvent records every delivered event (spike check 7).
func (p *mindmapPlugin) OnEvent(_ context.Context, e *pluginsdk.Event) error {
	keys := make([]string, 0, len(e.Payload))
	for k := range e.Payload {
		keys = append(keys, k)
	}
	entry := map[string]any{
		"event_id":     e.EventID,
		"event_type":   e.EventType,
		"workspace_id": e.WorkspaceID,
		"payload_keys": keys,
	}
	for _, k := range []string{"task_id", "session_id", "new_state", "old_state", "state", "status"} {
		if v, ok := e.Payload[k]; ok {
			entry[k] = v
		}
	}
	return p.appendLog("events", entry)
}
