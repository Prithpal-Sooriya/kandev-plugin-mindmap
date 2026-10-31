package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"kandev-plugin-mindmap/server/contract"
)

func toMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}

// maxBodyInRead is where read summarises long bodies.
const maxBodyInRead = 2000

// line renders one outline line:
// - [nd_x] idea · running · 2 src · conf 0.6 · Title
func line(n *contract.Node, depth int, bodies bool) string {
	var b strings.Builder
	b.WriteString(strings.Repeat("  ", depth))
	fmt.Fprintf(&b, "- [%s] %s", n.ID, n.Kind)
	if n.Status != contract.StatusIdle && n.Status != "" {
		b.WriteString(" · " + string(n.Status))
	}
	if len(n.Sources) > 0 {
		fmt.Fprintf(&b, " · %d src", len(n.Sources))
	}
	if n.Confidence != nil {
		fmt.Fprintf(&b, " · conf %s", strconv.FormatFloat(*n.Confidence, 'f', -1, 64))
	}
	if len(n.Assets) > 0 {
		fmt.Fprintf(&b, " · %d asset", len(n.Assets))
	}
	if n.Question != nil {
		if n.Question.Open() {
			b.WriteString(" · open question")
		} else {
			b.WriteString(" · answered")
		}
	}
	b.WriteString(" · " + n.Title)
	if bodies {
		ind := strings.Repeat("  ", depth+1)
		if n.BodyMD != "" {
			body := n.BodyMD
			if len(body) > maxBodyInRead {
				body = body[:maxBodyInRead] + fmt.Sprintf(" … (%d more chars)", len(n.BodyMD)-maxBodyInRead)
			}
			for _, l := range strings.Split(body, "\n") {
				b.WriteString("\n" + ind + "> " + l)
			}
		}
		if len(n.Tags) > 0 {
			b.WriteString("\n" + ind + "tags: " + strings.Join(n.Tags, ", "))
		}
		for _, s := range n.Sources {
			b.WriteString("\n" + ind + "source: " + s.URL)
			if s.Title != "" {
				b.WriteString(" — " + s.Title)
			}
		}
		if n.Question != nil && n.Question.Answer != "" {
			b.WriteString("\n" + ind + "answer: " + n.Question.Answer)
		}
	}
	return b.String()
}

// outline walks the subtree of start (depth levels) in sibling order.
func outline(nodes map[string]*contract.Node, start string, depth int, bodies bool) []string {
	kids := map[string][]*contract.Node{}
	for _, n := range nodes {
		if !n.Deleted && n.ParentID != "" {
			kids[n.ParentID] = append(kids[n.ParentID], n)
		}
	}
	for _, ks := range kids {
		sort.Slice(ks, func(i, j int) bool {
			if ks[i].Order != ks[j].Order {
				return ks[i].Order < ks[j].Order
			}
			return ks[i].ID < ks[j].ID
		})
	}
	var out []string
	var walk func(n *contract.Node, d int)
	walk = func(n *contract.Node, d int) {
		out = append(out, line(n, d, bodies))
		if d >= depth {
			if c := len(kids[n.ID]); c > 0 {
				out = append(out, fmt.Sprintf("%s  … %d more children", strings.Repeat("  ", d), c))
			}
			return
		}
		for _, c := range kids[n.ID] {
			walk(c, d+1)
		}
	}
	if n := nodes[start]; n != nil && !n.Deleted {
		walk(n, 0)
	}
	return out
}

func pathOf(nodes map[string]*contract.Node, id string) []string {
	var titles []string
	cur := nodes[id]
	for i := 0; cur != nil && i < 200; i++ {
		titles = append(titles, cur.Title)
		cur = nodes[cur.ParentID]
	}
	slices.Reverse(titles)
	return titles
}

func (t *Tools) context(_ context.Context, s *session, a any) (contract.ToolResult, error) {
	args := a.(*contract.ContextArgs)
	if s.personal() {
		maps, err := t.svc.Store.ListMaps("", "")
		if err != nil {
			return contract.ToolResult{}, err
		}
		var b strings.Builder
		b.WriteString("personal token: you act as the host with no run. Write tools need node_id or parent_id.\nmaps:\n")
		for _, m := range maps {
			fmt.Fprintf(&b, "- {%s} %s · %d nodes · workspace %s\n", m.ID, m.Title, m.Nodes, m.WorkspaceID)
		}
		return contract.ToolResult{Text: b.String(), Structured: map[string]any{"maps": maps}}, nil
	}
	r := s.run
	depth := args.SubtreeDepth
	if depth == 0 {
		depth = 2
	}
	var b strings.Builder
	fmt.Fprintf(&b, "run %s · template %s", r.ID, r.Template)
	if r.Lens != "" {
		fmt.Fprintf(&b, " · lens %s", r.Lens)
	}
	fmt.Fprintf(&b, " · mode %s · status %s\n", r.Mode, r.Status)
	if r.Mode == contract.ModePropose {
		b.WriteString("propose mode: your writes become proposal items a human reviews; refs and ids you create still work in later calls.\n")
	}
	if r.Goal != "" {
		fmt.Fprintf(&b, "goal: %s\n", r.Goal)
	}
	var rev int64
	var questions, claims []string
	err := t.svc.Store.View(s.mapID, func(m contract.Map, nodes map[string]*contract.Node, _ map[string]*contract.Link) error {
		rev = m.Rev
		n := nodes[r.NodeID]
		if n == nil {
			return contract.Errf(contract.ErrNotFound, "your node %s no longer exists", r.NodeID)
		}
		fmt.Fprintf(&b, "map {%s} \"%s\" rev %d\n", m.ID, m.Title, m.Rev)
		fmt.Fprintf(&b, "your node: [%s] %s", n.ID, n.Title)
		if n.Deleted {
			b.WriteString(" (deleted)")
		}
		b.WriteString("\npath: " + strings.Join(pathOf(nodes, n.ID), " > ") + "\n")
		if n.BodyMD != "" {
			body := n.BodyMD
			if len(body) > maxBodyInRead {
				body = body[:maxBodyInRead] + " …"
			}
			b.WriteString("node body:\n" + body + "\n")
		}
		b.WriteString("subtree:\n" + strings.Join(outline(nodes, n.ID, depth, false), "\n") + "\n")
		now := t.now()
		for _, x := range nodes {
			if x.Deleted {
				continue
			}
			if x.Question != nil && x.Question.RunID == r.ID {
				q := fmt.Sprintf("- [%s] %s", x.ID, x.Title)
				if x.Question.Open() {
					q += " (waiting for an answer)"
				} else {
					q += " → answer: " + x.Question.Answer
				}
				questions = append(questions, q)
			}
			if x.Claim != nil && x.Claim.ParticipantID != s.actor.ParticipantID && now.Before(x.Claim.ExpiresAt) {
				claims = append(claims, fmt.Sprintf("- [%s] %s claimed until %s %s", x.ID, x.Title, x.Claim.ExpiresAt.Format("15:04"), x.Claim.Note))
			}
		}
		return nil
	})
	if err != nil {
		return contract.ToolResult{}, err
	}
	bl := t.budget(s)
	if bl != nil {
		fmt.Fprintf(&b, "budget left: %d nodes · %d sub-agents · max depth %d (-1 = unlimited)\n", bl.Nodes, bl.Subagents, bl.Depth)
	}
	if len(questions) > 0 {
		b.WriteString("your questions:\n" + strings.Join(questions, "\n") + "\n")
	}
	if len(claims) > 0 {
		sort.Strings(claims)
		if len(claims) > 30 {
			claims = claims[:30]
		}
		b.WriteString("claimed by others (avoid duplicating their work):\n" + strings.Join(claims, "\n") + "\n")
	}
	return contract.ToolResult{Text: b.String(), Structured: map[string]any{
		"run_id": r.ID, "map_id": s.mapID, "node_id": r.NodeID, "mode": r.Mode, "rev": rev, "budget": bl,
	}}, nil
}

func (t *Tools) read(_ context.Context, s *session, a any) (contract.ToolResult, error) {
	args := a.(*contract.ReadArgs)
	mapID := args.MapID
	start := ""
	if args.NodeID != "" {
		id, err := t.nodeID(s, "node_id", args.NodeID, "")
		if err != nil {
			return contract.ToolResult{}, err
		}
		nm, err := t.svc.Store.NodeMap(id)
		if err != nil {
			return contract.ToolResult{}, contract.FieldErr(contract.ErrNotFound, "node_id", "node %s not found", id)
		}
		if mapID != "" && nm != mapID {
			return contract.ToolResult{}, contract.FieldErr(contract.ErrBadRequest, "node_id", "node %s is not in map %s", id, mapID)
		}
		mapID, start = nm, id
	}
	if mapID == "" {
		mapID = s.mapID
	}
	if mapID == "" {
		return contract.ToolResult{}, contract.FieldErr(contract.ErrBadRequest, "map_id", "required with a personal token")
	}
	if err := t.readMap(s, mapID); err != nil {
		return contract.ToolResult{}, err
	}
	depth := args.Depth
	if depth == 0 {
		depth = contract.DefaultReadDepth
	}
	limit := args.Limit
	if limit == 0 {
		limit = contract.DefaultReadLimit
	}
	offset := 0
	if args.Cursor != "" {
		v, err := strconv.Atoi(strings.TrimPrefix(args.Cursor, "o"))
		if err != nil || v < 0 || !strings.HasPrefix(args.Cursor, "o") {
			return contract.ToolResult{}, contract.FieldErr(contract.ErrBadRequest, "cursor", "pass next_cursor from a previous read unchanged")
		}
		offset = v
	}
	var lines []string
	var rev int64
	var title string
	err := t.svc.Store.View(mapID, func(m contract.Map, nodes map[string]*contract.Node, _ map[string]*contract.Link) error {
		rev, title = m.Rev, m.Title
		if start == "" {
			start = m.RootID
		}
		if n := nodes[start]; n == nil || n.Deleted {
			return contract.FieldErr(contract.ErrDeleted, "node_id", "node %s was deleted", start)
		}
		lines = outline(nodes, start, depth, args.IncludeBodies)
		return nil
	})
	if err != nil {
		return contract.ToolResult{}, err
	}
	// Page by top-level outline lines (body lines stay with their node).
	total := len(lines)
	if offset > total {
		offset = total
	}
	end := min(offset+limit, total)
	page := lines[offset:end]
	st := map[string]any{"rev": rev, "map_id": mapID}
	text := fmt.Sprintf("map {%s} \"%s\" rev %d\n", mapID, title, rev) + strings.Join(page, "\n")
	if end < total {
		st["next_cursor"] = fmt.Sprintf("o%d", end)
		text += fmt.Sprintf("\n… %d more lines: call read again with cursor \"o%d\"", total-end, end)
	}
	return contract.ToolResult{Text: text, Structured: st}, nil
}

func (t *Tools) search(_ context.Context, s *session, a any) (contract.ToolResult, error) {
	args := a.(*contract.SearchArgs)
	scope := args.Scope
	if scope == "" {
		scope = contract.ScopeMap
	}
	limit := args.Limit
	if limit == 0 {
		limit = contract.DefaultSearchLimit
	}
	sub := ""
	if args.NodeID != "" {
		id, err := t.nodeID(s, "node_id", args.NodeID, "")
		if err != nil {
			return contract.ToolResult{}, err
		}
		nm, err := t.svc.Store.NodeMap(id)
		if err != nil {
			return contract.ToolResult{}, contract.FieldErr(contract.ErrNotFound, "node_id", "node %s not found", id)
		}
		if err := t.readMap(s, nm); err != nil {
			return contract.ToolResult{}, err
		}
		if s.mapID == "" {
			s.mapID, s.workspaceID = nm, ""
			if ws, err := t.svc.Store.MapWorkspace(nm); err == nil {
				s.workspaceID = ws
			}
		}
		sub = id
	}
	if s.mapID == "" {
		return contract.ToolResult{}, contract.FieldErr(contract.ErrBadRequest, "node_id", "required with a personal token (it picks the map)")
	}
	maps := []string{s.mapID}
	if scope == contract.ScopeWorkspace {
		ws, err := t.svc.Store.WorkspaceMaps(s.workspaceID)
		if err != nil {
			return contract.ToolResult{}, err
		}
		maps = ws
	}
	if scope == contract.ScopeSubtree {
		if nm, _ := t.svc.Store.NodeMap(sub); nm != "" {
			maps = []string{nm}
		}
	}
	var order []string
	if args.Query != "" {
		hits, err := t.svc.Store.SearchText(maps, args.Query, 500)
		if err != nil {
			return contract.ToolResult{}, err
		}
		for _, h := range hits {
			order = append(order, h.MapID+"/"+h.NodeID)
		}
	}
	type hit struct {
		mapID string
		n     contract.Node
		path  []string
	}
	var results []hit
	for _, mid := range maps {
		_ = t.svc.Store.View(mid, func(m contract.Map, nodes map[string]*contract.Node, _ map[string]*contract.Link) error {
			idx := contract.ParentIndex{}
			for id, n := range nodes {
				if !n.Deleted {
					idx[id] = n.ParentID
				}
			}
			consider := func(n *contract.Node) {
				if n == nil || n.Deleted {
					return
				}
				if len(args.Kinds) > 0 && !slices.Contains(args.Kinds, n.Kind) {
					return
				}
				if len(args.Statuses) > 0 && !slices.Contains(args.Statuses, n.Status) {
					return
				}
				for _, tg := range args.Tags {
					if !slices.Contains(n.Tags, tg) {
						return
					}
				}
				if scope == contract.ScopeSubtree && n.ID != sub && !idx.IsAncestor(sub, n.ID) {
					return
				}
				results = append(results, hit{mapID: m.ID, n: *n, path: pathOf(nodes, n.ParentID)})
			}
			if args.Query != "" {
				for _, k := range order {
					if strings.HasPrefix(k, m.ID+"/") {
						consider(nodes[strings.TrimPrefix(k, m.ID+"/")])
					}
				}
			} else {
				ids := make([]string, 0, len(nodes))
				for id := range nodes {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				for _, id := range ids {
					consider(nodes[id])
				}
			}
			return nil
		})
	}
	if args.Query != "" {
		rank := map[string]int{}
		for i, k := range order {
			rank[k] = i
		}
		sort.SliceStable(results, func(i, j int) bool {
			return rank[results[i].mapID+"/"+results[i].n.ID] < rank[results[j].mapID+"/"+results[j].n.ID]
		})
	}
	more := len(results) > limit
	if more {
		results = results[:limit]
	}
	var b strings.Builder
	ids := []string{}
	for _, r := range results {
		l := line(&r.n, 0, false)
		if scope == contract.ScopeWorkspace {
			l += fmt.Sprintf(" {%s}", r.mapID)
		}
		if len(r.path) > 0 {
			l += "  (in " + strings.Join(r.path, " > ") + ")"
		}
		b.WriteString(l + "\n")
		ids = append(ids, r.n.ID)
	}
	if len(results) == 0 {
		b.WriteString("no matches\n")
	} else if more {
		fmt.Fprintf(&b, "… more matches; narrow the query or raise limit (max %d)\n", contract.MaxSearchLimit)
	}
	return contract.ToolResult{Text: b.String(), Structured: map[string]any{"node_ids": ids}}, nil
}
