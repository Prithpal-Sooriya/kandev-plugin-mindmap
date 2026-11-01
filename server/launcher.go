package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"

	"kandev-plugin-mindmap/server/app"
	"kandev-plugin-mindmap/server/contract"
)

// hostLauncher starts a run's Kandev task with Host.Tasks().Create.
type hostLauncher struct{ p *mindmapPlugin }

func (l hostLauncher) Launch(ctx context.Context, req app.LaunchRequest) (app.LaunchResult, error) {
	h := l.p.Host()
	if h == nil {
		return app.LaunchResult{}, fmt.Errorf("host not connected")
	}
	if req.Run.ProfileID == "" {
		return app.LaunchResult{}, fmt.Errorf("no agent profile")
	}
	profile, prompt := req.Run.ProfileID, req.Prompt
	meta := map[string]any{"mindmap_id": req.Run.MapID, "node_id": req.Run.NodeID, "run_id": req.Run.ID}
	if req.Run.ParentRunID != "" {
		meta["parent_run_id"] = req.Run.ParentRunID
	}
	in := pluginsdk.CreateTaskInput{
		WorkspaceID: l.p.workspaceFor(ctx, req.Run.WorkspaceID),
		Title:       req.Title,
		Description: req.Description,
		StartAgent:  true,
		Launch:      &pluginsdk.PluginTaskLaunchOptions{AgentProfileID: &profile, Prompt: &prompt},
		Metadata:    meta,
	}
	if req.RepoID != "" {
		in.Repositories = []pluginsdk.PluginTaskRepository{{RepositoryID: req.RepoID}}
	}
	task, err := h.Tasks().Create(ctx, in)
	if err != nil {
		return app.LaunchResult{}, err
	}
	if task == nil || task.ID == "" {
		return app.LaunchResult{}, fmt.Errorf("kandev returned no task")
	}
	return app.LaunchResult{TaskID: task.ID}, nil
}

// hostCache memoises Host lists that the run dialog and every launch read.
type hostCache struct {
	mu         sync.Mutex
	profiles   []contract.AgentProfile
	profilesAt time.Time
	workspaces map[string]bool
	wsAt       time.Time
}

const hostCacheTTL = 30 * time.Second

// workspaceFor returns ws when it is a real Kandev workspace, else "" so
// Kandev picks its default (maps created outside a workspace say "default").
func (p *mindmapPlugin) workspaceFor(ctx context.Context, ws string) string {
	if ws == "" {
		return ""
	}
	c := &p.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.workspaces == nil || time.Since(c.wsAt) > hostCacheTTL {
		h := p.Host()
		if h == nil {
			return ws
		}
		all := map[string]bool{}
		page := pluginsdk.Page{Limit: 100}
		for range 20 {
			items, info, err := h.Workspaces().List(ctx, page)
			if err != nil {
				return ws // cannot check: trust the map
			}
			for _, it := range items {
				all[it.ID] = true
			}
			if info == nil || !info.HasMore {
				break
			}
			page.Cursor = info.NextCursor
		}
		c.workspaces, c.wsAt = all, time.Now()
	}
	if c.workspaces[ws] {
		return ws
	}
	return ""
}

// cachedProfiles is listProfiles behind a short cache (each launch resolves
// its profile).
func (p *mindmapPlugin) cachedProfiles(ctx context.Context) ([]contract.AgentProfile, error) {
	c := &p.cache
	c.mu.Lock()
	if c.profiles != nil && time.Since(c.profilesAt) < hostCacheTTL {
		out := c.profiles
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()
	list, err := p.listProfiles(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.profiles, c.profilesAt = list, time.Now()
	c.mu.Unlock()
	return list, nil
}

// defaultProfile is the plugin config's agent_profile, falling back to a pi/cursor/claude profile.
func (p *mindmapPlugin) defaultProfile(ctx context.Context) string {
	h := p.Host()
	if h == nil {
		return ""
	}
	cfg, err := h.GetConfig(ctx)
	if err == nil {
		if v, _ := cfg["agent_profile"].(string); v != "" {
			if trimmed := strings.TrimSpace(v); trimmed != "" {
				return trimmed
			}
		}
	}

	profiles, err := p.cachedProfiles(ctx)
	if err != nil || len(profiles) == 0 {
		return ""
	}

	for _, p := range profiles {
		if p.Harness == contract.HarnessPi {
			return p.ID
		}
	}
	for _, p := range profiles {
		if p.Harness == contract.HarnessCursor {
			return p.ID
		}
	}
	for _, p := range profiles {
		if p.Harness == contract.HarnessClaude {
			return p.ID
		}
	}
	return profiles[0].ID
}

// configExportDir is the plugin config's export_dir.
func (p *mindmapPlugin) configExportDir(ctx context.Context) string {
	h := p.Host()
	if h == nil {
		return ""
	}
	cfg, err := h.GetConfig(ctx)
	if err != nil {
		return ""
	}
	v, _ := cfg["export_dir"].(string)
	return strings.TrimSpace(v)
}

// invokeUtility runs quick suggest's one-shot completion. An empty profile
// uses Kandev's default utility profile.
func (p *mindmapPlugin) invokeUtility(ctx context.Context, prompt, profileID string) (string, error) {
	h := p.Host()
	if h == nil {
		return "", fmt.Errorf("host not connected")
	}
	if profileID == "" {
		return h.InvokeUtilityAgent(ctx, prompt)
	}
	return h.InvokeUtilityAgent(ctx, prompt, pluginsdk.UtilityAgentOptions{ProfileID: profileID})
}

func (p *mindmapPlugin) listRepos(ctx context.Context, workspaceID string) ([]contract.RepoInfo, error) {
	h := p.Host()
	if h == nil {
		return nil, fmt.Errorf("host not connected")
	}
	ws := p.workspaceFor(ctx, workspaceID)
	if ws == "" {
		return []contract.RepoInfo{}, nil
	}
	out := []contract.RepoInfo{}
	page := pluginsdk.Page{Limit: 100}
	for range 20 {
		items, info, err := h.Repositories().List(ctx, ws, page)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			out = append(out, contract.RepoInfo{ID: it.ID, Name: it.Name})
		}
		if info == nil || !info.HasMore {
			break
		}
		page.Cursor = info.NextCursor
	}
	return out, nil
}

// --- early events ------------------------------------------------------------------

// earlyEvents holds events for tasks with no run yet. Tasks().Create can
// start the agent before it returns the task id, so the first session
// states may arrive before the run knows its task; they are replayed once
// the task id is recorded.
type earlyEvents struct {
	mu    sync.Mutex
	tasks map[string][]earlyEvent
}

type earlyEvent struct {
	at time.Time
	e  *pluginsdk.Event
}

const (
	earlyEventTTL     = 2 * time.Minute
	earlyEventTasks   = 200
	earlyEventPerTask = 50
)

func (b *earlyEvents) add(taskID string, e *pluginsdk.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tasks == nil {
		b.tasks = map[string][]earlyEvent{}
	}
	now := time.Now()
	if _, ok := b.tasks[taskID]; !ok && len(b.tasks) >= earlyEventTasks {
		for id, evs := range b.tasks {
			if len(evs) == 0 || now.Sub(evs[len(evs)-1].at) > earlyEventTTL {
				delete(b.tasks, id)
			}
		}
		if len(b.tasks) >= earlyEventTasks {
			return
		}
	}
	if len(b.tasks[taskID]) < earlyEventPerTask {
		b.tasks[taskID] = append(b.tasks[taskID], earlyEvent{at: now, e: e})
	}
}

func (b *earlyEvents) take(taskID string) []*pluginsdk.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	evs := b.tasks[taskID]
	delete(b.tasks, taskID)
	var out []*pluginsdk.Event
	for _, x := range evs {
		if time.Since(x.at) <= earlyEventTTL {
			out = append(out, x.e)
		}
	}
	return out
}

// onTaskBound replays the events that arrived before a run's task id was
// recorded.
func (p *mindmapPlugin) onTaskBound(taskID string) {
	for _, e := range p.early.take(taskID) {
		_ = p.OnEvent(context.Background(), e)
	}
}

// watchTimeBudgets stops runs past max_minutes, every 30 s.
func (p *mindmapPlugin) watchTimeBudgets(stop <-chan struct{}) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			p.svc.EnforceTimeBudgets(context.Background(), now.UTC())
		}
	}
}
