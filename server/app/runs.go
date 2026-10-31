package app

import (
	"context"
	"fmt"
	"strings"

	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/store"
)

// StartRun creates a run (data side: row, agent participant, run token,
// proposal, budget charge for children), asks the Launcher for a Kandev
// task, records the task id and broadcasts. With NoopLauncher the run stays
// queued without a task; Phase 2a plugs in the Host-backed launcher.
func (s *Service) StartRun(ctx context.Context, nr store.NewRun) (store.RunRecord, error) {
	run, token, err := s.Store.CreateRun(nr, s.Port())
	if err != nil {
		return run, err
	}
	node, _ := s.Store.Node(run.MapID, run.NodeID)
	if p, err := s.Store.GetParticipant(run.ParticipantID); err == nil {
		s.Hub.Publish(run.MapID, contract.Message{Type: contract.MsgParticipant, Payload: &contract.ParticipantPayload{Participant: p}})
	}
	s.PublishRun(run)
	if run.ProposalID != "" {
		if p, err := s.Store.GetProposal(run.ProposalID); err == nil {
			s.Hub.Publish(run.MapID, contract.Message{Type: contract.MsgProposal, Payload: &contract.ProposalPayload{Proposal: p}})
		}
	}
	typ, verb := contract.ActivityRunStarted, "started"
	actor := contract.Actor{ParticipantID: run.StartedBy}
	if run.ParentRunID != "" {
		typ, verb = contract.ActivityRunSpawned, "spawned"
		if parent, err := s.Store.GetRun(run.ParentRunID); err == nil {
			actor = contract.Actor{ParticipantID: parent.ParticipantID, RunID: parent.ID}
		}
	}
	s.Activity(contract.ActivityEvent{MapID: run.MapID, Type: typ, Actor: actor, NodeIDs: []string{run.NodeID},
		Summary: fmt.Sprintf("%s a %s agent on \"%s\"", verb, label(run), node.Title)})
	s.SetRunNodeStatus(run)
	res, err := s.Launcher.Launch(ctx, LaunchRequest{Run: run, Title: "Mind map: " + node.Title, Prompt: BuildPrompt(run, node, token), RunToken: token})
	if err != nil {
		run, _ = s.UpdateRun(run.ID, func(r *store.RunRecord) bool {
			r.Status, r.Error = contract.RunFailed, "launch failed: "+err.Error()
			return true
		})
		s.SetRunNodeStatus(run)
		return run, contract.Errf(contract.ErrUnavailable, "could not launch the agent task: %v", err)
	}
	if res.TaskID != "" {
		if err := s.Store.SetRunTask(run.ID, res.TaskID, res.SessionID); err != nil {
			return run, err
		}
		run.TaskID, run.SessionID = res.TaskID, res.SessionID
		s.PublishRun(run)
	}
	return run, nil
}

func label(r store.RunRecord) string {
	if r.Lens != "" {
		return strings.ReplaceAll(string(r.Lens), "_", " ")
	}
	return string(r.Template)
}

// BuildPrompt is the launch prompt. It tells the agent to load the tool
// schemas first (Claude defers MCP schemas and otherwise calls tools with
// {}), and carries the run token for the bridge.
func BuildPrompt(r store.RunRecord, node contract.Node, token string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are a %s agent working in a shared mind map (Kandev Mind Map plugin) on node [%s] \"%s\".\n", label(r), node.ID, node.Title)
	if r.Goal != "" {
		fmt.Fprintf(&b, "Goal: %s\n", r.Goal)
	}
	fmt.Fprintf(&b, "Mode: %s. ", r.Mode)
	if r.Mode == contract.ModePropose {
		b.WriteString("Your writes become a proposal a human reviews.\n")
	} else {
		b.WriteString("Your writes go live on the canvas.\n")
	}
	fmt.Fprintf(&b, "Budget: up to %d new nodes, %d levels deep, %d sub-agents.\n\n", r.Budget.MaxNodes, r.Budget.MaxDepth, r.Budget.MaxSubagents)
	b.WriteString("First load the mind map tool schemas (their names end in _context, _read, _search, _add, _update, _attach, _restructure, " +
		"_link, _claim, _spawn, _ask_human, _write_doc, _report), then call the `context` tool. Pass every required argument.\n")
	b.WriteString("Cite sources on research and example nodes. When you are finished, call `report` with a summary.\n\n")
	b.WriteString("If you have no mind map tools, use the bridge with this run token (MINDMAP_TOKEN):\n")
	b.WriteString("```mindmap-run-token\n" + token + "\n```\n")
	b.WriteString("If you have neither, reply with ```mindmap-patch``` JSON blocks: {\"v\":1,\"calls\":[{\"tool\":\"add\",\"args\":{...}}]}.\n")
	return b.String()
}
