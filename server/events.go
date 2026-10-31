package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"

	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/store"
	"kandev-plugin-mindmap/server/tools"
)

func str(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

// OnEvent maps Kandev events onto runs. Events for tasks that are not map
// runs are ignored. Returning nil acknowledges the delivery; errors are
// returned only for transient failures worth a retry.
func (p *mindmapPlugin) OnEvent(ctx context.Context, e *pluginsdk.Event) error {
	if p.initErr != nil || e == nil {
		return nil
	}
	taskID := str(e.Payload, "task_id")
	if taskID == "" {
		return nil
	}
	run, err := p.store.RunByTask(taskID)
	if err != nil {
		// Not a mind map run, or a run whose task id is not recorded yet.
		switch e.EventType {
		case "task_session.state_changed", "agent.completed", "agent.failed", "message.added", "message.updated":
			p.early.add(taskID, e)
		}
		return nil
	}
	switch e.EventType {
	case "task_session.state_changed":
		p.onSessionState(run, str(e.Payload, "session_id"), str(e.Payload, "new_state"), sessionError(e.Payload))
	case "agent.completed":
		p.onSessionState(run, str(e.Payload, "session_id"), contract.SessionCompleted, "")
	case "agent.failed":
		p.onSessionState(run, str(e.Payload, "session_id"), contract.SessionFailed, sessionError(e.Payload))
	case "message.added", "message.updated":
		p.onMessage(ctx, run, e.EventType, e.Payload)
	}
	return nil
}

// sessionError is the agent error a session event carries, if any.
func sessionError(payload map[string]any) string {
	msg := str(payload, "error_message")
	if msg == "" {
		if md, ok := payload["session_metadata"].(map[string]any); ok {
			if le, ok := md["last_agent_error"].(map[string]any); ok {
				msg = str(le, "message")
			}
		}
	}
	msg = strings.TrimSpace(msg)
	if r := []rune(msg); len(r) > 500 {
		msg = string(r[:500])
	}
	return msg
}

// onSessionState applies contract.NextRunStatus and broadcasts. errMsg is
// kept as the run's error when the run fails.
func (p *mindmapPlugin) onSessionState(run store.RunRecord, sessionID, newState, errMsg string) {
	if newState == "" {
		return // some state_changed payloads lack new_state: ignore
	}
	open, _ := p.store.OpenQuestions(run.MapID, run.ID)
	var changed bool
	r, err := p.svc.UpdateRun(run.ID, func(r *store.RunRecord) bool {
		st, wr, ok := contract.NextRunStatus(tools.Facts(p.store, *r, open), newState)
		if sessionID != "" && r.SessionID == "" {
			r.SessionID = sessionID
			changed = true
		}
		if ok && (st != r.Status || wr != r.WaitingReason) {
			r.Status, r.WaitingReason = st, wr
			changed = true
		}
		if errMsg != "" && r.Error == "" && r.Status == contract.RunFailed {
			r.Error = errMsg
			changed = true
		}
		return changed
	})
	if err != nil || !changed {
		return
	}
	p.svc.SetRunNodeStatus(r)
	p.svc.Activity(contract.ActivityEvent{MapID: r.MapID, Type: contract.ActivityRunStatus, Actor: contract.Actor{ParticipantID: r.ParticipantID, RunID: r.ID},
		Summary: fmt.Sprintf("run is %s", statusText(r)), NodeIDs: []string{r.NodeID}})
}

func statusText(r store.RunRecord) string {
	if r.WaitingReason != "" {
		return string(r.Status) + " (" + strings.ReplaceAll(string(r.WaitingReason), "_", " ") + ")"
	}
	return string(r.Status)
}

func outputKind(t string) contract.OutputLineKind {
	switch {
	case strings.Contains(t, "thinking"):
		return contract.OutputThinking
	case strings.Contains(t, "tool"):
		return contract.OutputTool
	case strings.Contains(t, "status"), strings.Contains(t, "progress"), strings.Contains(t, "error"):
		return contract.OutputStatus
	}
	return contract.OutputText
}

// onMessage turns agent messages into output lines and, for prompt-fallback
// runs, executes their mindmap-patch blocks.
func (p *mindmapPlugin) onMessage(ctx context.Context, run store.RunRecord, eventType string, pl map[string]any) {
	if str(pl, "author_type") != "agent" {
		return
	}
	content := str(pl, "content")
	if eventType == "message.added" && strings.TrimSpace(content) != "" {
		text := content
		if len(text) > 4000 {
			text = text[:4000] + " …"
		}
		p.svc.RecordOutput(run, []contract.OutputLine{{TS: time.Now().UTC(), Kind: outputKind(str(pl, "type")), Text: text}})
	}
	// Bridge runs (Pi, unknown harnesses) carry the patch fallback too, in
	// case the agent has no bridge tools.
	if run.Transport == contract.TransportPrompt || run.Transport == contract.TransportBridge {
		p.runPatches(ctx, run, str(pl, "message_id"), content)
	}
}

// patchBlocks returns the bodies of every closed ```mindmap-patch fence, in
// order (the same scan as contract.ExtractPatches, keeping block indexes).
func patchBlocks(text string) []string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(t, "```") || strings.TrimSpace(strings.TrimPrefix(t, "```")) != contract.PatchFence {
			continue
		}
		var body []string
		closed := false
		for i++; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "```" {
				closed = true
				break
			}
			body = append(body, lines[i])
		}
		if !closed {
			break // still streaming
		}
		out = append(out, strings.Join(body, "\n"))
	}
	return out
}

// runPatches executes each new block once, as the run, and sends one
// feedback message if any call failed.
func (p *mindmapPlugin) runPatches(ctx context.Context, run store.RunRecord, messageID, content string) {
	if messageID == "" {
		return
	}
	var failures []string
	refs := map[string]string{}
	for idx, body := range patchBlocks(content) {
		fresh, err := p.store.ClaimPatchBlock(run.ID, messageID, idx)
		if err != nil || !fresh {
			continue
		}
		var errs []string
		patch, err := contract.ParsePatch([]byte(body))
		if err != nil {
			errs = append(errs, fmt.Sprintf("block %d: %s", idx, contract.ToolError(contract.AsError(err)).Text))
		} else {
			for ci, call := range patch.Calls {
				res := p.tools.Invoke(ctx, tools.Caller{Transport: contract.TransportPrompt, RunID: run.ID}, string(call.Tool), call.Args)
				if res.IsError {
					errs = append(errs, fmt.Sprintf("block %d call %d (%s): %s", idx, ci, call.Tool, res.Text))
					continue
				}
				if r, ok := res.Structured["refs"].(map[string]any); ok {
					for k, v := range r {
						if s, ok := v.(string); ok {
							refs[k] = s
						}
					}
				}
			}
		}
		_ = p.store.FinishPatchBlock(run.ID, messageID, idx, errs)
		failures = append(failures, errs...)
	}
	if len(failures) == 0 || run.TaskID == "" {
		return
	}
	var b strings.Builder
	b.WriteString("Some mindmap-patch calls failed. Fix them and send only the failed calls again:\n")
	for _, f := range failures {
		b.WriteString("- " + f + "\n")
	}
	if len(refs) > 0 {
		rb, _ := json.Marshal(refs)
		b.WriteString("Refs created so far: " + string(rb) + "\n")
	}
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := p.svc.Messenger.Send(sctx, run.TaskID, run.SessionID, b.String()); err != nil {
		p.svc.Logf("mindmap: patch feedback for run %s: %v", run.ID, err)
	}
}
