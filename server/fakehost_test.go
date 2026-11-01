package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// fakeHost is the Kandev Host API as the plugin uses it: profiles,
// workspaces, repositories, task creation and messages. Everything is
// recorded for assertions.
type fakeHost struct {
	pluginsdk.UnimplementedHostData

	mu        sync.Mutex
	profiles  []pluginsdk.AgentProfile
	created   []pluginsdk.CreateTaskInput
	taskIDs   []string
	sent      []sentMessage
	config    map[string]any
	createErr error
	sendErr   error
	// createCtxErrs is ctx.Err() as each Create saw it.
	createCtxErrs []error
	// onCreate runs inside Create before it returns (simulates events that
	// arrive before the task id is known).
	onCreate func(taskID string)
	// utility answers InvokeUtilityAgent (quick suggest).
	utility func(prompt, profileID string) (string, error)
}

type sentMessage struct{ TaskID, SessionID, Text string }

func newFakeHost() *fakeHost {
	return &fakeHost{
		profiles: []pluginsdk.AgentProfile{
			{ID: "prof-claude", AgentID: "a1b2c3d4-0000-0000-0000-000000000001", DisplayName: "Claude", Name: "Claude Researcher"},
			{ID: "prof-cursor", AgentID: "a1b2c3d4-0000-0000-0000-000000000002", DisplayName: "Cursor", Name: "Cursor Fast"},
			{ID: "prof-pi", AgentID: "a1b2c3d4-0000-0000-0000-000000000003", DisplayName: "Pi", Name: "Pi Flash"},
			{ID: "prof-copilot", AgentID: "a1b2c3d4-0000-0000-0000-000000000004", DisplayName: "Copilot", Name: "Copilot Default"},
		},
		config: map[string]any{},
	}
}

func (h *fakeHost) Created() []pluginsdk.CreateTaskInput {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]pluginsdk.CreateTaskInput(nil), h.created...)
}

func (h *fakeHost) Sent() []sentMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]sentMessage(nil), h.sent...)
}

func (h *fakeHost) GetState(context.Context, string, string, string) (map[string]any, bool, error) {
	return nil, false, nil
}
func (h *fakeHost) SetState(context.Context, string, string, string, map[string]any) error {
	return nil
}
func (h *fakeHost) DeleteState(context.Context, string, string, string) error { return nil }
func (h *fakeHost) ListState(context.Context, string, string) ([]pluginsdk.StateEntry, error) {
	return nil, nil
}
func (h *fakeHost) GetConfig(context.Context) (map[string]any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]any{}
	for k, v := range h.config {
		out[k] = v
	}
	return out, nil
}
func (h *fakeHost) RevealSecret(context.Context, string) (string, error)    { return "", nil }
func (h *fakeHost) GetSecret(context.Context, string) (string, bool, error) { return "", false, nil }
func (h *fakeHost) SetSecret(context.Context, string, string) error         { return nil }
func (h *fakeHost) DeleteSecret(context.Context, string) error              { return nil }
func (h *fakeHost) EmitEvent(context.Context, string, map[string]any) error { return nil }
func (h *fakeHost) Tasks() pluginsdk.TaskReader                             { return fakeTasks{h} }
func (h *fakeHost) AgentProfiles() pluginsdk.AgentProfileReader             { return fakeProfiles{h} }
func (h *fakeHost) Messages() pluginsdk.MessageReader                       { return fakeMessages{h} }
func (h *fakeHost) Workspaces() pluginsdk.WorkspaceReader                   { return fakeWorkspaces{} }
func (h *fakeHost) Repositories() pluginsdk.RepositoryReader                { return fakeRepos{} }
func (h *fakeHost) InvokeUtilityAgent(_ context.Context, prompt string, opts ...pluginsdk.UtilityAgentOptions) (string, error) {
	h.mu.Lock()
	fn := h.utility
	h.mu.Unlock()
	if fn == nil {
		return "", errors.New("not in tests")
	}
	profile := ""
	if len(opts) > 0 {
		profile = opts[0].ProfileID
	}
	return fn(prompt, profile)
}

type fakeTasks struct{ h *fakeHost }

func (t fakeTasks) List(context.Context, pluginsdk.TaskFilter, pluginsdk.Page) ([]pluginsdk.Task, *pluginsdk.PageInfo, error) {
	return nil, nil, nil
}
func (t fakeTasks) Get(_ context.Context, id string) (*pluginsdk.Task, error) {
	return &pluginsdk.Task{ID: id}, nil
}
func (t fakeTasks) Create(ctx context.Context, in pluginsdk.CreateTaskInput) (*pluginsdk.Task, error) {
	t.h.mu.Lock()
	t.h.createCtxErrs = append(t.h.createCtxErrs, ctx.Err())
	if t.h.createErr != nil {
		err := t.h.createErr
		t.h.mu.Unlock()
		return nil, err
	}
	id := fmt.Sprintf("task-%d", len(t.h.created)+1)
	t.h.created = append(t.h.created, in)
	t.h.taskIDs = append(t.h.taskIDs, id)
	cb := t.h.onCreate
	t.h.mu.Unlock()
	if cb != nil {
		cb(id)
	}
	return &pluginsdk.Task{ID: id, WorkspaceID: in.WorkspaceID, Title: in.Title}, nil
}
func (t fakeTasks) Update(context.Context, pluginsdk.UpdateTaskInput) (*pluginsdk.Task, error) {
	return nil, errors.New("not in tests")
}
func (t fakeTasks) Move(context.Context, pluginsdk.MoveTaskInput) (*pluginsdk.MoveTaskOutcome, error) {
	return nil, errors.New("not in tests")
}

type fakeProfiles struct{ h *fakeHost }

func (p fakeProfiles) List(context.Context, pluginsdk.Page) ([]pluginsdk.AgentProfile, *pluginsdk.PageInfo, error) {
	p.h.mu.Lock()
	defer p.h.mu.Unlock()
	return append([]pluginsdk.AgentProfile(nil), p.h.profiles...), nil, nil
}

type fakeMessages struct{ h *fakeHost }

func (m fakeMessages) List(context.Context, pluginsdk.MessageFilter, pluginsdk.Page) ([]pluginsdk.Message, *pluginsdk.PageInfo, error) {
	return nil, nil, nil
}
func (m fakeMessages) Send(_ context.Context, taskID, sessionID, text string) (*pluginsdk.MessageDispatch, error) {
	m.h.mu.Lock()
	defer m.h.mu.Unlock()
	if m.h.sendErr != nil {
		return nil, m.h.sendErr
	}
	m.h.sent = append(m.h.sent, sentMessage{taskID, sessionID, text})
	return &pluginsdk.MessageDispatch{SessionID: sessionID, Status: "sent"}, nil
}

type fakeWorkspaces struct{}

func (fakeWorkspaces) List(context.Context, pluginsdk.Page) ([]pluginsdk.Workspace, *pluginsdk.PageInfo, error) {
	return []pluginsdk.Workspace{{ID: "ws-1", Name: "Main"}}, nil, nil
}

type fakeRepos struct{}

func (fakeRepos) List(_ context.Context, ws string, _ pluginsdk.Page) ([]pluginsdk.Repository, *pluginsdk.PageInfo, error) {
	if ws != "ws-1" {
		return nil, nil, nil
	}
	return []pluginsdk.Repository{{ID: "repo-1", WorkspaceID: ws, Name: "example-app"}}, nil, nil
}
