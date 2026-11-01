package web

import (
	"bytes"
	"net/http"
	"strconv"
	"time"

	"kandev-plugin-mindmap/server/app"
	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/share"
)

// SuggestWait is how long POST suggest waits for the utility agent before
// answering 202 with the job to poll. It stays under the 25 s `api` webhook
// timeout.
var SuggestWait = 18 * time.Second

// startCompile starts a compile: the coordinator, the doc placeholders and
// the first wave of section runs.
func (s *Server) startCompile(w http.ResponseWriter, r *http.Request, p *principal) {
	mapID := r.PathValue("map")
	var req contract.CompileRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.Mode != "" && !req.Mode.Valid() {
		writeErr(w, contract.FieldErr(contract.ErrBadRequest, "mode", "must be apply or propose"))
		return
	}
	if len(req.Slug) > 80 {
		writeErr(w, contract.FieldErr(contract.ErrTooLarge, "slug", "at most 80 characters"))
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
	runs, err := s.svc.StartCompile(r.Context(), app.CompileSpec{MapID: mapID, NodeID: req.NodeID, ProfileID: req.ProfileID, Slug: req.Slug,
		Target: req.Target, RepoID: req.RepoID, Mode: req.Mode, StartedBy: pt.ID})
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

// startSuggest runs quick suggest. It answers 200 with the proposed
// suggestions when the utility agent is quick, else 202 with the job id to
// poll at GET .../suggest/<job>.
func (s *Server) startSuggest(w http.ResponseWriter, r *http.Request, p *principal) {
	mapID := r.PathValue("map")
	var req contract.SuggestRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, err)
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
	run, done, err := s.svc.StartSuggest(r.Context(), mapID, req.NodeID, req.Count, req.ProfileID, pt.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	select {
	case <-done:
	case <-time.After(SuggestWait):
	case <-r.Context().Done():
	}
	_, res, err := s.svc.SuggestResult(run.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	status := 200
	if res.Status == contract.SuggestRunning {
		status = http.StatusAccepted
	}
	writeJSON(w, status, res)
}

// suggestJob polls a suggest job.
func (s *Server) suggestJob(w http.ResponseWriter, r *http.Request, p *principal) {
	run, res, err := s.svc.SuggestResult(r.PathValue("job"))
	if err == nil && run.MapID != r.PathValue("map") {
		err = contract.Errf(contract.ErrNotFound, "suggest job not found")
	}
	if err != nil {
		if p.kind == contract.TokenShare {
			notFound(w)
			return
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

// docTrees lists the map's compiled doc trees, newest first.
func (s *Server) docTrees(w http.ResponseWriter, r *http.Request, _ *principal) {
	trees, err := s.svc.DocTrees(r.PathValue("map"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, trees)
}

// exportZip downloads a doc tree (?node_id=<doc>, default the newest) as a
// zip built from the doc nodes, so it works before any file is written.
func (s *Server) exportZip(w http.ResponseWriter, r *http.Request, _ *principal) {
	tree, files, err := s.svc.ExportFiles(r.PathValue("map"), r.URL.Query().Get("node_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var buf bytes.Buffer
	if err := app.WriteZip(&buf, tree.Slug, files); err != nil {
		writeErr(w, contract.Errf(contract.ErrInternal, "build the zip: %v", err))
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+tree.Slug+`.zip"`)
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = w.Write(buf.Bytes())
}

// exportDisk writes a doc tree (?node_id=, default the newest) to the export
// folder on the host's disk. Host only.
func (s *Server) exportDisk(w http.ResponseWriter, r *http.Request, p *principal) {
	if !hostOnly(w, p) {
		return
	}
	mapID := r.PathValue("map")
	if _, err := s.svc.ExportToDisk(r.Context(), mapID, r.URL.Query().Get("node_id")); err != nil {
		writeErr(w, err)
		return
	}
	trees, err := s.svc.DocTrees(mapID)
	if err != nil {
		writeErr(w, err)
		return
	}
	id := r.URL.Query().Get("node_id")
	for _, t := range trees {
		if id == "" || containsDoc(t.Index, id) || t.Single != nil && t.Single.NodeID == id {
			writeJSON(w, 200, t)
			return
		}
	}
	writeErr(w, contract.Errf(contract.ErrNotFound, "doc tree not found"))
}

func containsDoc(e contract.DocEntry, id string) bool {
	if e.NodeID == id {
		return true
	}
	for _, c := range e.Children {
		if containsDoc(c, id) {
			return true
		}
	}
	return false
}
