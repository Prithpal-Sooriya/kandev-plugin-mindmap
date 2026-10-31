package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/kandev/kandev/pkg/pluginsdk"

	"kandev-plugin-mindmap/server/app"
	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/hub"
	"kandev-plugin-mindmap/server/store"
	"kandev-plugin-mindmap/server/tools"
	"kandev-plugin-mindmap/server/web"
)

// mindmapPlugin is the Kandev side of the plugin: agent tools, webhooks and
// events. The real work happens in app.Service; the loopback server (web)
// serves the canvas clients and the bridge.
type mindmapPlugin struct {
	pluginsdk.UnimplementedPlugin

	dataDir string
	store   *store.Store
	hub     *hub.Hub
	svc     *app.Service
	tools   *tools.Dispatcher
	impl    *tools.Tools
	web     *web.Server
	initErr error
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
	p := &mindmapPlugin{dataDir: dir}
	if err := p.init(); err != nil {
		p.initErr = err
		log.Printf("mindmap: init failed: %v", err)
	}
	return p
}

func (p *mindmapPlugin) init() error {
	if err := os.MkdirAll(p.dataDir, 0o755); err != nil {
		return err
	}
	st, err := store.Open(p.dataDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	p.store, p.hub = st, hub.New()
	p.svc = app.New(st, p.hub)
	p.svc.Messenger = hostMessenger{p}
	p.tools, p.impl = tools.NewDispatcher(p.svc)
	p.web = web.New(p.svc, p.tools)
	p.web.Profiles = p.listProfiles
	if _, err := p.web.Start(); err != nil {
		return fmt.Errorf("start loopback server: %w", err)
	}
	return nil
}

// close releases resources (tests).
func (p *mindmapPlugin) close() {
	if p.web != nil {
		_ = p.web.Close()
	}
	if p.hub != nil {
		p.hub.Close()
	}
	if p.store != nil {
		_ = p.store.Close()
	}
}

// hostMessenger sends chat messages to run agents through the Host API.
type hostMessenger struct{ p *mindmapPlugin }

func (m hostMessenger) Send(ctx context.Context, taskID, sessionID, text string) error {
	h := m.p.Host()
	if h == nil {
		return fmt.Errorf("host not connected")
	}
	_, err := h.Messages().Send(ctx, taskID, sessionID, text)
	return err
}

func harnessOf(agentID string) contract.Harness {
	a := strings.ToLower(agentID)
	for _, h := range []contract.Harness{contract.HarnessClaude, contract.HarnessCursor, contract.HarnessCodex, contract.HarnessPi} {
		if strings.Contains(a, string(h)) {
			return h
		}
	}
	return contract.HarnessOther
}

func (p *mindmapPlugin) listProfiles(ctx context.Context) ([]contract.AgentProfile, error) {
	h := p.Host()
	if h == nil {
		return nil, fmt.Errorf("host not connected")
	}
	out := []contract.AgentProfile{}
	page := pluginsdk.Page{Limit: 100}
	for range 20 {
		items, info, err := h.AgentProfiles().List(ctx, page)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			name := it.DisplayName
			if name == "" {
				name = it.Name
			}
			out = append(out, contract.AgentProfile{ID: it.ID, Name: name, Harness: harnessOf(it.AgentID)})
		}
		if info == nil || !info.HasMore {
			break
		}
		page.Cursor = info.NextCursor
	}
	return out, nil
}

// InvokeAgentTool routes a Kandev MCP tool call to the shared tool
// implementation. Access is decided by the plugin's task_id -> run table.
func (p *mindmapPlugin) InvokeAgentTool(ctx context.Context, req *pluginsdk.AgentToolRequest) (*pluginsdk.AgentToolResult, error) {
	if p.initErr != nil {
		res := contract.ToolError(contract.Errf(contract.ErrUnavailable, "mind map plugin failed to start: %v", p.initErr))
		return &pluginsdk.AgentToolResult{Text: res.Text, StructuredContent: res.Structured, IsError: true}, nil
	}
	res := p.tools.InvokeMap(ctx, tools.Caller{
		Transport:    contract.TransportMCP,
		InvocationID: req.InvocationID,
		TaskID:       req.Context.TaskID,
		SessionID:    req.Context.SessionID,
		WorkspaceID:  req.Context.WorkspaceID,
	}, req.Name, req.Arguments)
	return &pluginsdk.AgentToolResult{Text: res.Text, StructuredContent: res.Structured, IsError: res.IsError}, nil
}
