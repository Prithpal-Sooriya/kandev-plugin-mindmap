package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"kandev-plugin-mindmap/server/app"
	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/store"
)

// Tools implements the 13 agent tools against the store. One instance
// serves Kandev MCP (InvokeAgentTool), the bridge HTTP route and
// mindmap-patch blocks.
type Tools struct {
	svc *app.Service
	// FileRoots are the directories attach.file_path may read from. The
	// plugin is not told the agent's worktree, so it allows the Kandev task
	// workspaces dir and the temp dirs (see docs/protocol.md §8.2).
	FileRoots []string
	// AllowPrivateURLs lets attach.url fetch loopback/private addresses (tests).
	AllowPrivateURLs bool
	now              func() time.Time
}

// New returns the tool implementations.
func New(svc *app.Service) *Tools {
	return &Tools{svc: svc, FileRoots: DefaultFileRoots(), now: func() time.Time { return time.Now().UTC() }}
}

// NewDispatcher returns a dispatcher with every tool registered.
func NewDispatcher(svc *app.Service) (*Dispatcher, *Tools) {
	t := New(svc)
	d := NewStub()
	t.Register(d)
	svc.Outline = t.Outline
	return d, t
}

// Register installs the real handlers into d.
func (t *Tools) Register(d *Dispatcher) {
	d.Register(contract.ToolContext, t.wrap(t.context))
	d.Register(contract.ToolRead, t.wrap(t.read))
	d.Register(contract.ToolSearch, t.wrap(t.search))
	d.Register(contract.ToolAdd, t.wrap(t.add))
	d.Register(contract.ToolUpdate, t.wrap(t.update))
	d.Register(contract.ToolAttach, t.wrap(t.attach))
	d.Register(contract.ToolRestructure, t.wrap(t.restructure))
	d.Register(contract.ToolLink, t.wrap(t.link))
	d.Register(contract.ToolClaim, t.wrap(t.claim))
	d.Register(contract.ToolSpawn, t.wrap(t.spawn))
	d.Register(contract.ToolAskHuman, t.wrap(t.askHuman))
	d.Register(contract.ToolWriteDoc, t.wrap(t.writeDoc))
	d.Register(contract.ToolReport, t.wrap(t.report))
}

// session is a resolved caller: a run, or the host via a personal token.
type session struct {
	run         *store.RunRecord // nil for personal tokens
	mapID       string           // the run's map ("" for personal until a node picks it)
	workspaceID string
	actor       contract.Actor
	refs        map[string]string // refs set earlier in this call
	pending     map[string]bool   // node ids created earlier in this call
}

func (s *session) personal() bool { return s.run == nil }

type handler func(ctx context.Context, s *session, args any) (contract.ToolResult, error)

func (t *Tools) wrap(h handler) Handler {
	return func(ctx context.Context, c Caller, args any) (contract.ToolResult, error) {
		s, err := t.resolve(c)
		if err != nil {
			return contract.ToolResult{}, err
		}
		ctx, cancel := context.WithTimeout(ctx, contract.AgentToolTimeout)
		defer cancel()
		res, err := h(ctx, s, args)
		if err != nil {
			return res, err
		}
		if len(res.Text) > maxResultText {
			res.Text = res.Text[:maxResultText] + "\n… (truncated; narrow the request)"
		}
		return res, nil
	}
}

// maxResultText keeps results well under Kandev's 1 MiB cap with room for
// structured content.
const maxResultText = 600 << 10

var errNotARun = contract.Errf(contract.ErrNotARun, "This task is not a mind map run. Start it from a node in the Mind Map plugin.")

// resolve authenticates the caller and enforces run grace.
func (t *Tools) resolve(c Caller) (*session, error) {
	st := t.svc.Store
	var run store.RunRecord
	var err error
	switch c.Transport {
	case contract.TransportMCP:
		if run, err = st.RunByTask(c.TaskID); err != nil {
			return nil, errNotARun
		}
	case contract.TransportBridge:
		info, lerr := st.LookupToken(c.Token)
		if lerr != nil {
			return nil, contract.Errf(contract.ErrUnauthorized, "missing, expired or revoked bridge token")
		}
		switch info.Kind {
		case contract.TokenRun:
			if run, err = st.GetRun(info.RunID); err != nil {
				return nil, errNotARun
			}
		case contract.TokenPersonal:
			return &session{refs: map[string]string{}, pending: map[string]bool{}}, nil
		default:
			return nil, contract.Errf(contract.ErrUnauthorized, "this token cannot call agent tools")
		}
	case contract.TransportPrompt:
		if run, err = st.GetRun(c.RunID); err != nil {
			return nil, errNotARun
		}
	default:
		return nil, errNotARun
	}
	if run.Status.Terminal() && !run.EndedAt.IsZero() && t.now().After(run.EndedAt.Add(contract.RunTokenGrace)) {
		return nil, contract.Errf(contract.ErrRunEnded, "this run has ended; ask a human to message you to continue")
	}
	return &session{run: &run, mapID: run.MapID, workspaceID: run.WorkspaceID,
		actor: contract.Actor{ParticipantID: run.ParticipantID, RunID: run.ID}, refs: map[string]string{}, pending: map[string]bool{}}, nil
}

// bindMap fixes the session's map for a personal token from the first node
// it names, and sets the actor to that map's host.
func (t *Tools) bindMap(s *session, mapID string) error {
	if s.mapID != "" {
		return nil
	}
	host, err := t.svc.Store.HostParticipant(mapID)
	if err != nil {
		return err
	}
	ws, err := t.svc.Store.MapWorkspace(mapID)
	if err != nil {
		return err
	}
	s.mapID, s.workspaceID, s.actor = mapID, ws, contract.Actor{ParticipantID: host.ID}
	return nil
}

// nodeID resolves a node argument (id or @ref) to an id without checking
// which map it is in. def is used when arg is empty.
func (t *Tools) nodeID(s *session, field, arg, def string) (string, error) {
	if arg == "" {
		arg = def
	}
	if arg == "" {
		if s.personal() {
			return "", contract.FieldErr(contract.ErrBadRequest, field, "required with a personal token (there is no run node)")
		}
		return "", contract.FieldErr(contract.ErrBadRequest, field, "required")
	}
	v, isRef := contract.SplitNodeRef(arg)
	if !isRef {
		return v, nil
	}
	if id, ok := s.refs[v]; ok {
		return id, nil
	}
	if s.personal() {
		return "", contract.FieldErr(contract.ErrBadRequest, field, "@refs need a run; use node ids with a personal token")
	}
	id, err := t.svc.Store.ResolveRef(s.run.RootRunID, v)
	if err != nil {
		e := *contract.AsError(err)
		e.Field = field
		return "", &e
	}
	return id, nil
}

// writeNode resolves a node the caller may write: it must be in the run's
// map (personal tokens bind the map here). Ghost nodes of the run's own
// proposal pass (they exist only in the proposal).
func (t *Tools) writeNode(s *session, field, arg, def string) (string, error) {
	id, err := t.nodeID(s, field, arg, def)
	if err != nil {
		return "", err
	}
	if s.pending[id] {
		return id, nil
	}
	m, err := t.svc.Store.NodeMap(id)
	if err != nil {
		if s.run != nil && s.run.Mode == contract.ModePropose && t.isGhost(s, id) {
			return id, nil
		}
		return "", contract.FieldErr(contract.ErrNotFound, field, "node %s not found", id)
	}
	if err := t.bindMap(s, m); err != nil {
		return "", err
	}
	if m != s.mapID {
		return "", contract.FieldErr(contract.ErrForbidden, field, "node %s is in another map; runs may only write their own map", id)
	}
	return id, nil
}

func (t *Tools) isGhost(s *session, id string) bool {
	p, err := t.svc.Store.GetProposal(s.run.ProposalID)
	if err != nil {
		return false
	}
	for _, it := range p.Items {
		if c, ok := it.Op.Payload.(*contract.NodeCreatePayload); ok && c.Node.ID == id && it.Status == contract.ItemPending {
			return true
		}
	}
	return false
}

// readMap checks that a map is readable: the run's own map or another map
// in the same workspace.
func (t *Tools) readMap(s *session, mapID string) error {
	if mapID == s.mapID {
		return nil
	}
	ws, err := t.svc.Store.MapWorkspace(mapID)
	if err != nil {
		return contract.FieldErr(contract.ErrNotFound, "map_id", "map %s not found", mapID)
	}
	if s.personal() && s.mapID == "" {
		return nil
	}
	if ws != s.workspaceID {
		return contract.FieldErr(contract.ErrForbidden, "map_id", "map %s is in another workspace", mapID)
	}
	return nil
}

func newOp(mapID string, p contract.OpPayload) contract.Op {
	return contract.Op{ID: store.NewID(contract.PrefixOp), MapID: mapID, Type: p.OpType(), Payload: p}
}

// commit applies ops live (apply mode, personal tokens, or live=true) or
// appends them to the run's proposal.
func (t *Tools) commit(s *session, ops []contract.Op, live bool) (contract.WriteOutcome, error) {
	out := contract.WriteOutcome{}
	if s.run != nil && s.run.Mode == contract.ModePropose && !live && s.run.ProposalID != "" {
		p, _, err := t.svc.Propose(s.run.ProposalID, ops, s.actor)
		if err != nil {
			return out, err
		}
		m, _ := t.svc.Store.GetMap(s.mapID)
		out.ProposalID, out.Rev = p.ID, m.Rev
	} else {
		res, err := t.svc.Submit(s.mapID, ops, app.SubmitOptions{Actor: s.actor})
		if err != nil {
			return out, err
		}
		out.Applied, out.Rev = true, res.Rev
	}
	out.Budget = t.budget(s)
	return out, nil
}

func (t *Tools) budget(s *session) *contract.BudgetLeft {
	if s.run == nil {
		return &contract.BudgetLeft{Nodes: -1, Subagents: -1, Depth: -1}
	}
	b, err := t.svc.Store.BudgetLeft(s.run.RootRunID)
	if err != nil {
		return nil
	}
	return &b
}

func (t *Tools) setRefs(s *session, refs map[string]string) {
	for r, id := range refs {
		s.refs[r] = id
		if s.run != nil {
			_ = t.svc.Store.SetRef(s.run.RootRunID, r, id, s.run.ID)
		}
	}
}

// focus moves the agent's presence to where it last wrote (follow mode).
func (t *Tools) focus(s *session, nodeID string) {
	if s.run == nil || nodeID == "" {
		return
	}
	t.svc.Hub.UpdatePresence(s.mapID, contract.Presence{ParticipantID: s.actor.ParticipantID, FocusNodeID: nodeID})
}

func (t *Tools) activity(s *session, typ contract.ActivityType, summary string, ids []string, count int, rev int64) {
	name := "Host"
	if p, err := t.svc.Store.GetParticipant(s.actor.ParticipantID); err == nil {
		name = p.Name
	}
	t.svc.Activity(contract.ActivityEvent{MapID: s.mapID, Type: typ, Actor: s.actor, Summary: name + " " + summary, NodeIDs: ids, Count: count, Rev: rev})
}

// outcomeResult renders a WriteOutcome.
func outcomeResult(text string, o contract.WriteOutcome) contract.ToolResult {
	if !o.Applied && o.ProposalID != "" {
		text += fmt.Sprintf("\nproposed in %s (not live until a human accepts it)", o.ProposalID)
	}
	if o.Budget != nil && o.Budget.Nodes >= 0 {
		text += fmt.Sprintf("\nbudget left: %d nodes, %d sub-agents", o.Budget.Nodes, o.Budget.Subagents)
	}
	return contract.ToolResult{Text: text, Structured: toMap(o)}
}

// title shortens s to a single-line title.
func title(s string) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if len(s) > contract.MaxTitleLen {
		cut := contract.MaxTitleLen - 3
		for cut > 0 && !utf8Start(s[cut]) {
			cut--
		}
		s = s[:cut] + "..."
	}
	if s == "" {
		s = "Untitled"
	}
	return s
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func uniq(ids []string) []string {
	var out []string
	for _, id := range ids {
		if id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
