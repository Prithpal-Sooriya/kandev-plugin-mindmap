package web

import (
	"context"
	"net/http"
	"time"

	"kandev-plugin-mindmap/server/app"
	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/share"
	"kandev-plugin-mindmap/server/store"
)

// startRun starts one run from a node: Run agent…, Explore, Garden,
// Critique, Execute and the other templates.
func (s *Server) startRun(w http.ResponseWriter, r *http.Request, p *principal) {
	mapID := r.PathValue("map")
	var req contract.StartRunRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.Template != "" && !req.Template.Valid() {
		writeErr(w, contract.FieldErr(contract.ErrBadRequest, "template", "unknown template %q", req.Template))
		return
	}
	if req.Mode != "" && !req.Mode.Valid() {
		writeErr(w, contract.FieldErr(contract.ErrBadRequest, "mode", "must be apply or propose"))
		return
	}
	if req.Lens != "" && !req.Lens.Valid() {
		writeErr(w, contract.FieldErr(contract.ErrBadRequest, "lens", "unknown lens"))
		return
	}
	if err := req.Budget.Validate("budget"); err != nil {
		writeErr(w, err)
		return
	}
	if len(req.Goal) > contract.MaxQuestionLen {
		writeErr(w, contract.FieldErr(contract.ErrTooLarge, "goal", "at most %d characters", contract.MaxQuestionLen))
		return
	}
	pt, err := s.participant(p, r, mapID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.limits.Allow(share.LimitRuns, s.runLimitKey(r, p)); err != nil {
		writeErr(w, err)
		return
	}
	m, err := s.svc.Store.GetMap(mapID)
	if err != nil {
		writeErr(w, err)
		return
	}
	nodeID := req.NodeID
	if nodeID == "" && req.Template == contract.TemplateGarden {
		nodeID = m.RootID // the gardener tends the whole map by default
	}
	repo := req.RepoID
	if req.Template == contract.TemplateExecute {
		n, err := s.svc.Store.Node(mapID, nodeID)
		if err != nil {
			writeErr(w, contract.FieldErr(contract.ErrNotFound, "node_id", "node %s not found", nodeID))
			return
		}
		if n.Kind != contract.KindAction {
			writeErr(w, contract.FieldErr(contract.ErrBadRequest, "node_id", "execute works on action nodes; this node is a %s", n.Kind))
			return
		}
		if repo == "" {
			repo = m.RepoID
		}
	}
	profile := req.ProfileID
	if profile == "" {
		profile = m.Settings.DefaultProfileID
	}
	run, err := s.svc.StartRun(r.Context(), app.RunSpec{NewRun: store.NewRun{MapID: mapID, NodeID: nodeID, ProfileID: profile, Template: req.Template,
		Lens: req.Lens, Mode: req.Mode, Goal: req.Goal, Budget: req.Budget, StartedBy: pt.ID}, RepoID: repo})
	if err != nil && run.ID == "" {
		writeErr(w, err)
		return
	}
	// A failed launch still returns the run (status failed, with its error).
	writeJSON(w, 200, contract.StartRunsResponse{Runs: []contract.Run{run.Run}})
}

// startSwarm starts a lens swarm: a coordinator run plus one lens run per
// member, each on its own new sub-branch.
func (s *Server) startSwarm(w http.ResponseWriter, r *http.Request, p *principal) {
	mapID := r.PathValue("map")
	var req contract.SwarmRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if len(req.Goal) > contract.MaxQuestionLen {
		writeErr(w, contract.FieldErr(contract.ErrTooLarge, "goal", "at most %d characters", contract.MaxQuestionLen))
		return
	}
	pt, err := s.participant(p, r, mapID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.limits.Allow(share.LimitRuns, s.runLimitKey(r, p)); err != nil {
		writeErr(w, err)
		return
	}
	m, err := s.svc.Store.GetMap(mapID)
	if err != nil {
		writeErr(w, err)
		return
	}
	runs, err := s.svc.StartSwarm(r.Context(), app.SwarmSpec{MapID: mapID, NodeID: req.NodeID, Goal: req.Goal, Mode: req.Mode, Budget: req.Budget,
		Agents: req.Agents, ProfileID: m.Settings.DefaultProfileID, StartedBy: pt.ID})
	if err != nil && len(runs) == 0 {
		writeErr(w, err)
		return
	}
	out := contract.StartRunsResponse{Runs: make([]contract.Run, len(runs))}
	for i, x := range runs {
		out.Runs[i] = x.Run
	}
	writeJSON(w, 200, out)
}

// runFor loads a run the principal may act on. Runs of other maps look
// missing (404, empty for share tokens).
func (s *Server) runFor(w http.ResponseWriter, r *http.Request, p *principal) (store.RunRecord, contract.Participant, bool) {
	run, err := s.svc.Store.GetRun(r.PathValue("run"))
	if err != nil || !p.canAccess(run.MapID) {
		if p.kind == contract.TokenShare {
			notFound(w)
		} else {
			writeErr(w, contract.Errf(contract.ErrNotFound, "run not found"))
		}
		return run, contract.Participant{}, false
	}
	pt, err := s.participant(p, r, run.MapID)
	if err != nil {
		writeErr(w, err)
		return run, pt, false
	}
	return run, pt, true
}

// stopRun asks a run's agent to stop (protocol §9: there is no Host stop
// API), revokes its run token and marks it stopped.
func (s *Server) stopRun(w http.ResponseWriter, r *http.Request, p *principal) {
	run, pt, ok := s.runFor(w, r, p)
	if !ok {
		return
	}
	if _, err := s.svc.StopRun(r.Context(), run.ID, pt.ID, ""); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// approveRun launches a guest run held in waiting(approval); on a swarm
// coordinator it launches every held lens run. Host only.
func (s *Server) approveRun(w http.ResponseWriter, r *http.Request, p *principal) {
	if !hostOnly(w, p) {
		return
	}
	run, _, ok := s.runFor(w, r, p)
	if !ok {
		return
	}
	runs, err := s.svc.ApproveRun(r.Context(), run.ID)
	if err != nil && len(runs) == 0 {
		writeErr(w, err)
		return
	}
	out := contract.StartRunsResponse{Runs: make([]contract.Run, len(runs))}
	for i, x := range runs {
		out.Runs[i] = x.Run
	}
	writeJSON(w, 200, out)
}

// messageRun sends a follow-up message to a run's agent.
func (s *Server) messageRun(w http.ResponseWriter, r *http.Request, p *principal) {
	var req contract.MessageRunRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	run, pt, ok := s.runFor(w, r, p)
	if !ok {
		return
	}
	if err := s.svc.MessageRun(r.Context(), run.ID, pt.ID, req.Text); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// answerQuestion answers an ask_human question node as the caller. The
// answer op is broadcast like any other and relayed to the asking agent.
func (s *Server) answerQuestion(w http.ResponseWriter, r *http.Request, p *principal) {
	mapID := r.PathValue("map")
	var req contract.AnswerQuestionRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if len(req.Answer) > contract.MaxBodyLen {
		writeErr(w, contract.FieldErr(contract.ErrTooLarge, "answer", "at most %d characters", contract.MaxBodyLen))
		return
	}
	pt, err := s.participant(p, r, mapID)
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.svc.AnswerQuestion(mapID, r.PathValue("node"), req.Answer, pt.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, contract.OpsPayload{Ops: res.Committed, Rev: res.Rev})
}

// runTasks lists the task ids of map runs (task panel visibility). Local
// all-maps tokens only: it spans every map.
func (s *Server) runTasks(w http.ResponseWriter, _ *http.Request, p *principal) {
	if !p.host || p.scope != "" {
		writeErr(w, contract.Errf(contract.ErrForbidden, "only an all-maps local token can list run tasks"))
		return
	}
	ids, err := s.svc.Store.RunTaskIDs()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, contract.RunTasksResponse{TaskIDs: ids})
}

// taskRun is the run behind a Kandev task, for the task panel. Local
// tokens only, and only for maps the token covers.
func (s *Server) taskRun(w http.ResponseWriter, r *http.Request, p *principal) {
	if !p.host {
		notFound(w)
		return
	}
	info, err := s.svc.TaskRunInfo(r.PathValue("task"))
	if err == nil && !p.canAccess(info.Run.MapID) {
		err = contract.Errf(contract.ErrNotFound, "task is not a run of this map")
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, info)
}

// repos lists the Kandev repositories of a map's workspace (?map_id=, or
// the token's map), for the Run agent dialog.
func (s *Server) repos(w http.ResponseWriter, r *http.Request, p *principal) {
	mapID := p.scope
	if mapID == "" {
		mapID = r.URL.Query().Get("map_id")
	}
	if mapID == "" || !p.canAccess(mapID) {
		writeErr(w, contract.FieldErr(contract.ErrBadRequest, "map_id", "required"))
		return
	}
	m, err := s.svc.Store.GetMap(mapID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if s.Repos == nil {
		writeJSON(w, 200, []contract.RepoInfo{})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	list, err := s.Repos(ctx, m.WorkspaceID)
	if err != nil {
		writeErr(w, contract.Errf(contract.ErrUnavailable, "repositories: %v", err))
		return
	}
	writeJSON(w, 200, list)
}
