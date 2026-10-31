package main

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

func jsonResp(status int32, v any) (*pluginsdk.WebhookResponse, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return &pluginsdk.WebhookResponse{Status: 500, Body: []byte(err.Error())}, nil
	}
	return &pluginsdk.WebhookResponse{
		Status:  status,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    b,
	}, nil
}

func errResp(status int32, msg string) (*pluginsdk.WebhookResponse, error) {
	return jsonResp(status, map[string]any{"error": msg})
}

// HandleWebhook dispatches the authenticated spike webhooks.
func (p *mindmapPlugin) HandleWebhook(ctx context.Context, req *pluginsdk.WebhookRequest) (*pluginsdk.WebhookResponse, error) {
	q, _ := url.ParseQuery(req.Query)
	switch req.WebhookKey {
	case "debug-launch":
		return p.debugLaunch(ctx, req)
	case "probe":
		return p.probe(ctx)
	case "agent-profiles":
		return p.agentProfiles(ctx)
	case "logs":
		return p.readLog(q.Get("name"))
	case "hold":
		secs, _ := strconv.Atoi(q.Get("secs"))
		start := time.Now()
		select {
		case <-time.After(time.Duration(secs) * time.Second):
		case <-ctx.Done():
			_ = p.appendLog("hold", map[string]any{"secs": secs, "cancelled_after": time.Since(start).String(), "err": ctx.Err().Error()})
			return errResp(499, "ctx cancelled")
		}
		_ = p.appendLog("hold", map[string]any{"secs": secs, "completed_after": time.Since(start).String()})
		return jsonResp(200, map[string]any{"held_secs": secs, "elapsed": time.Since(start).String()})
	case "echo-size":
		return jsonResp(200, map[string]any{"body_bytes": len(req.Body)})
	case "tunnel-test":
		return p.tunnelTest(ctx)
	case "task-ops":
		return p.taskOps(ctx, req)
	}
	return &pluginsdk.WebhookResponse{Status: 404}, nil
}

func (p *mindmapPlugin) readLog(name string) (*pluginsdk.WebhookResponse, error) {
	if name != "ping" && name != "events" && name != "hold" {
		return errResp(400, "name must be ping|events|hold")
	}
	b, err := os.ReadFile(filepath.Join(p.dataDir, name+".log"))
	if err != nil {
		return errResp(404, err.Error())
	}
	return &pluginsdk.WebhookResponse{Status: 200, Headers: map[string]string{"Content-Type": "text/plain"}, Body: b}, nil
}

func (p *mindmapPlugin) agentProfiles(ctx context.Context) (*pluginsdk.WebhookResponse, error) {
	host := p.Host()
	if host == nil {
		return errResp(503, "host not injected")
	}
	var all []pluginsdk.AgentProfile
	page := pluginsdk.Page{Limit: 100}
	for {
		items, info, err := host.AgentProfiles().List(ctx, page)
		if err != nil {
			return errResp(500, err.Error())
		}
		all = append(all, items...)
		if info == nil || !info.HasMore {
			break
		}
		page.Cursor = info.NextCursor
	}
	return jsonResp(200, all)
}

type debugLaunchBody struct {
	AgentProfileID string `json:"agent_profile_id"`
	Prompt         string `json:"prompt"`
	WorkspaceID    string `json:"workspace_id"`
	Title          string `json:"title"`
}

const defaultSpikePrompt = `Call the kandev mindmap_ping tool with {"msg":"hello"} and reply with its output. Do nothing else.`

// debugLaunch is the launch path the real plugin will use: Host.Tasks().Create
// with Launch{AgentProfileID, Prompt}, StartAgent and plugin Metadata.
func (p *mindmapPlugin) debugLaunch(ctx context.Context, req *pluginsdk.WebhookRequest) (*pluginsdk.WebhookResponse, error) {
	host := p.Host()
	if host == nil {
		return errResp(503, "host not injected")
	}
	var body debugLaunchBody
	if err := json.Unmarshal(req.Body, &body); err != nil || body.AgentProfileID == "" {
		return errResp(400, "body must be JSON with agent_profile_id")
	}
	if body.Prompt == "" {
		body.Prompt = defaultSpikePrompt
	}
	if body.Title == "" {
		body.Title = "mindmap spike: ping via " + body.AgentProfileID
	}
	profile, prompt := body.AgentProfileID, body.Prompt
	task, err := host.Tasks().Create(ctx, pluginsdk.CreateTaskInput{
		WorkspaceID: body.WorkspaceID,
		Title:       body.Title,
		Description: "Phase 0 spike task. Safe to delete.",
		StartAgent:  true,
		Launch:      &pluginsdk.PluginTaskLaunchOptions{AgentProfileID: &profile, Prompt: &prompt},
		Metadata:    map[string]any{"mindmap_id": "spike", "node_id": "spike", "run_id": "spike"},
	})
	if err != nil {
		return errResp(500, err.Error())
	}
	_ = p.appendLog("launch", map[string]any{"task_id": task.ID, "profile": profile})
	return jsonResp(200, task)
}

func (p *mindmapPlugin) taskOps(ctx context.Context, req *pluginsdk.WebhookRequest) (*pluginsdk.WebhookResponse, error) {
	host := p.Host()
	if host == nil {
		return errResp(503, "host not injected")
	}
	var body struct {
		Op     string `json:"op"`
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil || body.TaskID == "" {
		return errResp(400, "body must be JSON {op, task_id}")
	}
	switch body.Op {
	case "get":
		t, err := host.Tasks().Get(ctx, body.TaskID)
		if err != nil {
			return errResp(500, err.Error())
		}
		return jsonResp(200, t)
	case "delete":
		mgr, ok := pluginsdk.PluginOwnedTaskTrees(host)
		if !ok {
			return errResp(501, "host has no PluginOwnedTaskTrees extension")
		}
		ids, err := mgr.Delete(ctx, body.TaskID)
		if err != nil {
			return errResp(500, err.Error())
		}
		return jsonResp(200, map[string]any{"deleted": ids})
	}
	return errResp(400, "op must be get|delete")
}

// probe reports spike checks 3 and 7 (listener, subprocess, sqlite).
func (p *mindmapPlugin) probe(ctx context.Context) (*pluginsdk.WebhookResponse, error) {
	out := map[string]any{"data_dir": p.dataDir}
	srv, err := p.ensureServer()
	if err != nil {
		out["listen_error"] = err.Error()
	} else {
		out["listen_addr"] = srv.addr
		out["port"] = srv.port
		out["hello_url"] = "http://" + srv.addr + "/hello"
		out["ws_url"] = "ws://" + srv.addr + "/ws"
	}
	sub := map[string]any{}
	if o, err := exec.CommandContext(ctx, "/bin/echo", "subprocess-ok").CombinedOutput(); err != nil {
		sub["error"] = err.Error()
	} else {
		sub["echo"] = strings.TrimSpace(string(o))
	}
	if path, err := exec.LookPath("cloudflared"); err != nil {
		sub["cloudflared"] = "not found: " + err.Error()
	} else {
		sub["cloudflared"] = path
	}
	out["subprocess"] = sub
	out["sqlite"] = sqliteProbe(p.dataDir)
	return jsonResp(200, out)
}

// tunnelTest starts a cloudflared quick tunnel at the probe server for ~20s,
// fetches /hello through the public URL, and kills the tunnel. It does nothing
// unless cloudflared is already installed.
func (p *mindmapPlugin) tunnelTest(ctx context.Context) (*pluginsdk.WebhookResponse, error) {
	path, err := exec.LookPath("cloudflared")
	if err != nil {
		return jsonResp(200, map[string]any{"skipped": "cloudflared not installed"})
	}
	srv, err := p.ensureServer()
	if err != nil {
		return errResp(500, err.Error())
	}
	return jsonResp(200, runQuickTunnel(ctx, path, srv.addr, 20*time.Second))
}
