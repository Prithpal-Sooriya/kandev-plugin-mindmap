// Package tools routes agent tool calls from every transport (Kandev MCP,
// the /agent/v1 bridge API, and mindmap-patch blocks) to one implementation.
//
// NewStub returns a dispatcher whose handlers validate and then answer
// not_implemented; NewDispatcher (tools.go) registers the real
// implementations from Tools.
package tools

import (
	"context"
	"encoding/json"

	"kandev-plugin-mindmap/server/contract"
)

// Caller identifies who is calling. Exactly one of TaskID (MCP) or Token
// (bridge) is the credential; for patch blocks the backend fills RunID itself.
type Caller struct {
	Transport    contract.ToolTransport
	InvocationID string
	TaskID       string // Kandev MCP: resolve run via the task_id -> run table
	SessionID    string
	WorkspaceID  string
	Token        string // bridge: raw mmr1/mmp1 token from Authorization
	RunID        string // patch blocks: the run whose message carried the block
}

// Handler implements one tool. args is the decoded *contract.<Tool>Args.
// Return a *contract.Error for expected failures.
type Handler func(ctx context.Context, c Caller, args any) (contract.ToolResult, error)

// Dispatcher maps tool names to handlers.
type Dispatcher struct {
	handlers map[contract.ToolName]Handler
}

// NewStub returns a dispatcher whose 13 tools validate their arguments and
// then return not_implemented.
func NewStub() *Dispatcher {
	d := &Dispatcher{handlers: map[contract.ToolName]Handler{}}
	for _, name := range contract.ToolNames {
		d.handlers[name] = notImplemented(name)
	}
	return d
}

func notImplemented(name contract.ToolName) Handler {
	return func(context.Context, Caller, any) (contract.ToolResult, error) {
		return contract.ToolResult{}, contract.Errf(contract.ErrNotImplemented, "mindmap tool %q is not implemented yet", name)
	}
}

// Register replaces the handler for a known tool. It panics on unknown names
// so a typo fails at startup, not at call time.
func (d *Dispatcher) Register(name contract.ToolName, h Handler) {
	if _, ok := d.handlers[name]; !ok {
		panic("tools: unknown tool " + string(name))
	}
	d.handlers[name] = h
}

// Invoke decodes and validates rawArgs, then runs the handler. It never
// returns a Go error: failures become error results the model can read.
func (d *Dispatcher) Invoke(ctx context.Context, c Caller, name string, rawArgs []byte) contract.ToolResult {
	tn := contract.ToolName(name)
	h, ok := d.handlers[tn]
	if !ok {
		return contract.ToolError(contract.Errf(contract.ErrNotFound, "unknown mindmap tool %q", name))
	}
	args, err := contract.DecodeToolArgs(tn, rawArgs)
	if err != nil {
		return contract.ToolError(contract.AsError(err))
	}
	res, err := h(ctx, c, args)
	if err != nil {
		return contract.ToolError(contract.AsError(err))
	}
	if res.Text == "" {
		res.Text = "ok" // Kandev rejects empty result text
	}
	// Structured content crosses gRPC as a protobuf Struct, which only takes
	// plain JSON values: round-trip it so typed values become maps/strings.
	if res.Structured != nil {
		var plain map[string]any
		if b, err := json.Marshal(res.Structured); err == nil && json.Unmarshal(b, &plain) == nil {
			res.Structured = plain
		}
	}
	return res
}

// InvokeMap is Invoke for pluginsdk's map[string]any arguments.
func (d *Dispatcher) InvokeMap(ctx context.Context, c Caller, name string, args map[string]any) contract.ToolResult {
	if args == nil {
		args = map[string]any{}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return contract.ToolError(contract.Errf(contract.ErrBadRequest, "arguments are not JSON: %v", err))
	}
	return d.Invoke(ctx, c, name, raw)
}
