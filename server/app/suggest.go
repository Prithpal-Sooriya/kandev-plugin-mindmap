package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/store"
)

// Suggester runs a one-shot, non-interactive completion and returns its
// text (Host.InvokeUtilityAgent). An empty profile id uses Kandev's default
// utility profile.
type Suggester func(ctx context.Context, prompt, profileID string) (string, error)

const (
	DefaultSuggestCount = 5
	MaxSuggestCount     = 12
	// SuggestTag marks nodes that quick suggest proposed.
	SuggestTag = "suggested"
)

// SuggestTimeout bounds one utility-agent call.
var SuggestTimeout = 3 * time.Minute

// SuggestKinds are the kinds a suggestion may have; anything else becomes
// an idea.
var SuggestKinds = []contract.NodeKind{contract.KindIdea, contract.KindResearch, contract.KindExample, contract.KindQuestion,
	contract.KindDecision, contract.KindRisk, contract.KindAction}

// StartSuggest asks the utility agent for child nodes of a node. It
// creates a short "suggest" run (no Kandev task) in propose mode and calls
// the agent in the background; the returned channel closes when the
// suggestions are proposed (or the call failed). Results are read with
// SuggestResult. Guests on maps that hold guest runs for approval cannot
// use it.
func (s *Service) StartSuggest(ctx context.Context, mapID, nodeID string, count int, profileID, by string) (store.RunRecord, <-chan struct{}, error) {
	if s.Suggest == nil {
		return store.RunRecord{}, nil, contract.Errf(contract.ErrUnavailable, "quick suggest needs Kandev's utility agent, which is not connected")
	}
	node, err := s.Store.Node(mapID, nodeID)
	if err != nil || node.Deleted {
		return store.RunRecord{}, nil, contract.FieldErr(contract.ErrNotFound, "node_id", "node %s not found", nodeID)
	}
	if node.Kind == contract.KindDoc {
		return store.RunRecord{}, nil, contract.FieldErr(contract.ErrBadRequest, "node_id", "suggest works on map nodes, not on compiled docs")
	}
	if count <= 0 {
		count = DefaultSuggestCount
	}
	if count > MaxSuggestCount {
		return store.RunRecord{}, nil, contract.FieldErr(contract.ErrTooLarge, "count", "at most %d suggestions", MaxSuggestCount)
	}
	hold, err := s.needsApproval(store.NewRun{MapID: mapID, StartedBy: by})
	if err != nil {
		return store.RunRecord{}, nil, err
	}
	if hold {
		return store.RunRecord{}, nil, contract.Errf(contract.ErrForbidden, "the host approves guest agent runs on this map, so quick suggest is off for guests")
	}
	nr, err := s.clampGuestBudget(store.NewRun{MapID: mapID, StartedBy: by,
		Budget: &contract.Budget{MaxDepth: 1, MaxNodes: count, MaxSubagents: 1, MaxMinutes: 5}})
	if err != nil {
		return store.RunRecord{}, nil, err
	}
	if err := s.CheckRunCaps(mapID, nr.Budget, 1); err != nil {
		return store.RunRecord{}, nil, err
	}
	nr.NodeID, nr.Template, nr.Mode, nr.Harness, nr.Transport = nodeID, contract.TemplateSuggest, contract.ModePropose, contract.HarnessOther, contract.TransportMCP
	nr.Goal = fmt.Sprintf("Suggest %d child branches", count)
	nr.ProfileName = "default utility agent"
	if profileID != "" {
		nr.ProfileID, nr.ProfileName = profileID, profileID
		if s.Profiles != nil {
			list, err := s.Profiles(ctx)
			if err != nil {
				return store.RunRecord{}, nil, contract.Errf(contract.ErrUnavailable, "could not list agent profiles: %v", err)
			}
			p, ok := FindProfile(list, profileID)
			if !ok {
				return store.RunRecord{}, nil, contract.FieldErr(contract.ErrBadRequest, "profile_id", "unknown agent profile %q", profileID)
			}
			nr.ProfileID, nr.ProfileName, nr.Harness = p.ID, p.Name, p.Harness
		}
	}
	run, _, err := s.Store.CreateRun(nr, s.Port())
	if err != nil {
		return run, nil, err
	}
	_ = s.Store.RevokeRunTokens(run.ID) // the utility agent has no tools
	run, _ = s.UpdateRun(run.ID, func(r *store.RunRecord) bool {
		r.Status = contract.RunRunning
		return true
	})
	s.announceRun(run, node)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.runSuggest(run, node, count)
	}()
	return run, done, nil
}

func (s *Service) runSuggest(run store.RunRecord, node contract.Node, count int) {
	ctx, cancel := context.WithTimeout(context.Background(), SuggestTimeout)
	defer cancel()
	prompt, existing := s.suggestPrompt(run, node, count)
	text, err := s.Suggest(ctx, prompt, run.ProfileID)
	if cur, gerr := s.Store.GetRun(run.ID); gerr == nil && cur.Status.Terminal() {
		return // stopped meanwhile
	}
	if err != nil {
		s.failSuggest(run, "the utility agent failed: "+err.Error())
		return
	}
	var items []contract.Suggestion
	for _, it := range ParseSuggestions(text, MaxSuggestCount) {
		if !existing[strings.ToLower(it.Title)] && len(items) < count {
			items = append(items, it)
		}
	}
	if len(items) == 0 {
		s.failSuggest(run, "the utility agent returned no usable suggestions")
		return
	}
	last := ""
	_ = s.Store.View(run.MapID, func(_ contract.Map, nodes map[string]*contract.Node, _ map[string]*contract.Link) error {
		for _, n := range nodes {
			if !n.Deleted && n.ParentID == node.ID && n.Order > last {
				last = n.Order
			}
		}
		return nil
	})
	ops := make([]contract.Op, len(items))
	for i, it := range items {
		last = contract.OrderBetween(last, "")
		ops[i] = contract.Op{ID: store.NewID(contract.PrefixOp), MapID: run.MapID, Type: contract.OpNodeCreate, Payload: &contract.NodeCreatePayload{
			Node: contract.NodeInit{ID: store.NewID(contract.PrefixNode), ParentID: node.ID, Order: last, Title: it.Title, BodyMD: it.Body, Kind: it.Kind,
				Tags: []string{SuggestTag}}}}
	}
	actor := contract.Actor{ParticipantID: run.ParticipantID, RunID: run.ID}
	if _, _, err := s.Propose(run.ProposalID, ops, actor); err != nil {
		s.failSuggest(run, "could not propose the suggestions: "+err.Error())
		return
	}
	summary := fmt.Sprintf("Suggested %d branch%s for \"%s\"", len(items), map[bool]string{true: "", false: "es"}[len(items) == 1], node.Title)
	if err := s.Store.SetProposalReady(run.ProposalID, summary); err == nil {
		if p, err := s.Store.GetProposal(run.ProposalID); err == nil {
			s.Hub.Publish(run.MapID, contract.Message{Type: contract.MsgProposal, Payload: &contract.ProposalPayload{Proposal: p}})
		}
	}
	r, err := s.UpdateRun(run.ID, func(r *store.RunRecord) bool {
		if r.Status.Terminal() {
			return false
		}
		r.Status, r.Summary, r.Usage.Nodes, r.Reported = contract.RunDone, summary, len(items), true
		return true
	})
	if err == nil {
		s.SetRunNodeStatus(r)
	}
	s.Activity(contract.ActivityEvent{MapID: run.MapID, Type: contract.ActivityProposalCreated, Actor: actor, Count: len(items),
		Summary: "Quick suggest proposed " + strings.TrimPrefix(summary, "Suggested "), NodeIDs: []string{node.ID}})
}

func (s *Service) failSuggest(run store.RunRecord, msg string) {
	r, err := s.UpdateRun(run.ID, func(r *store.RunRecord) bool {
		if r.Status.Terminal() {
			return false
		}
		r.Status, r.Error = contract.RunFailed, msg
		return true
	})
	if err == nil {
		s.SetRunNodeStatus(r)
	}
	s.Logf("mindmap: suggest %s: %s", run.ID, msg)
}

// suggestPrompt asks for count children as JSON, and returns the lower-cased
// titles already under the node.
func (s *Service) suggestPrompt(run store.RunRecord, node contract.Node, count int) (string, map[string]bool) {
	existing := map[string]bool{}
	var kids, path []string
	mapTitle := ""
	_ = s.Store.View(run.MapID, func(m contract.Map, nodes map[string]*contract.Node, _ map[string]*contract.Link) error {
		mapTitle = m.Title
		for cur, i := nodes[node.ID], 0; cur != nil && i < 200; cur, i = nodes[cur.ParentID], i+1 {
			path = append([]string{cur.Title}, path...)
		}
		for _, c := range childIndex(nodes)[node.ID] {
			existing[strings.ToLower(c.Title)] = true
			if len(kids) < 40 {
				kids = append(kids, c.Title)
			}
		}
		return nil
	})
	var b strings.Builder
	b.WriteString("You suggest new child branches for one node of a mind map. Reply with JSON only.\n\n")
	fmt.Fprintf(&b, "Map: %q\nPath: %s\nNode: %q (%s)\n", mapTitle, strings.Join(path, " > "), node.Title, node.Kind)
	if body := strings.TrimSpace(node.BodyMD); body != "" {
		if r := []rune(body); len(r) > 1500 {
			body = string(r[:1500]) + "…"
		}
		b.WriteString("Notes on the node:\n" + body + "\n")
	}
	if len(kids) > 0 {
		b.WriteString("Existing children (do not repeat them):\n- " + strings.Join(kids, "\n- ") + "\n")
	}
	kinds := make([]string, len(SuggestKinds))
	for i, k := range SuggestKinds {
		kinds[i] = string(k)
	}
	fmt.Fprintf(&b, "\nSuggest %d new, distinct child nodes that would deepen this branch: sub-topics, open questions, risks, examples or next actions. "+
		"Each needs a short title (under 80 characters), a kind (one of %s) and a body of one or two sentences.\n\n", count, strings.Join(kinds, ", "))
	b.WriteString(`Reply with only a JSON array and no other text, for example: [{"title": "...", "kind": "idea", "body": "..."}]`)
	return b.String(), existing
}

// ParseSuggestions reads a utility agent's reply: a JSON array of
// {title, kind, body} (optionally fenced, or wrapped as {"suggestions":
// [...]}), else a bulleted list of titles. Titles are trimmed to one line and
// de-duplicated; unknown kinds become ideas. At most max are returned.
func ParseSuggestions(text string, max int) []contract.Suggestion {
	type raw struct {
		Title       string `json:"title"`
		Kind        string `json:"kind"`
		Body        string `json:"body"`
		Description string `json:"description"`
	}
	var items []raw
	if i, j := strings.Index(text, "["), strings.LastIndex(text, "]"); i >= 0 && j > i {
		_ = json.Unmarshal([]byte(text[i:j+1]), &items)
	}
	if len(items) == 0 {
		if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
			var wrap struct {
				Suggestions []raw `json:"suggestions"`
			}
			if json.Unmarshal([]byte(text[i:j+1]), &wrap) == nil {
				items = wrap.Suggestions
			}
		}
	}
	if len(items) == 0 {
		for _, line := range strings.Split(text, "\n") {
			l := strings.TrimSpace(line)
			for _, p := range []string{"- ", "* ", "• "} {
				if strings.HasPrefix(l, p) {
					items = append(items, raw{Title: strings.TrimSpace(l[len(p):])})
				}
			}
		}
	}
	out := []contract.Suggestion{}
	seen := map[string]bool{}
	for _, it := range items {
		t := truncTitle(strings.TrimSpace(strings.SplitN(strings.Trim(strings.TrimSpace(it.Title), "*#`\""), "\n", 2)[0]))
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		kind := contract.NodeKind(strings.ToLower(strings.TrimSpace(it.Kind)))
		valid := false
		for _, k := range SuggestKinds {
			valid = valid || k == kind
		}
		if !valid {
			kind = contract.KindIdea
		}
		body := strings.TrimSpace(it.Body)
		if body == "" {
			body = strings.TrimSpace(it.Description)
		}
		if r := []rune(body); len(r) > 2000 {
			body = string(r[:2000])
		}
		out = append(out, contract.Suggestion{Title: t, Kind: kind, Body: body})
		if len(out) >= max {
			break
		}
	}
	return out
}

// SuggestResult reads a suggest job (its run) as a SuggestResponse.
func (s *Service) SuggestResult(runID string) (store.RunRecord, contract.SuggestResponse, error) {
	run, err := s.Store.GetRun(runID)
	if err != nil || run.Template != contract.TemplateSuggest {
		return run, contract.SuggestResponse{}, contract.Errf(contract.ErrNotFound, "suggest job %s not found", runID)
	}
	res := contract.SuggestResponse{JobID: run.ID, ProposalID: run.ProposalID, Suggestions: []contract.Suggestion{}}
	switch run.Status {
	case contract.RunDone, contract.RunReview:
		res.Status = contract.SuggestDone
	case contract.RunFailed, contract.RunStopped:
		res.Status, res.Error = contract.SuggestFailed, run.Error
		if res.Error == "" {
			res.Error = "stopped"
		}
	default:
		res.Status = contract.SuggestRunning
	}
	if p, err := s.Store.GetProposal(run.ProposalID); err == nil {
		for _, it := range p.Items {
			if c, ok := it.Op.Payload.(*contract.NodeCreatePayload); ok {
				res.NodeIDs = append(res.NodeIDs, c.Node.ID)
				res.Suggestions = append(res.Suggestions, contract.Suggestion{Title: c.Node.Title, Kind: c.Node.Kind, Body: c.Node.BodyMD})
			}
		}
	}
	return run, res, nil
}
