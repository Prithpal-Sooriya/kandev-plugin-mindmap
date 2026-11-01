package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	contractfiles "kandev-plugin-mindmap/contract"
	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/share"
	"kandev-plugin-mindmap/server/store"
	"kandev-plugin-mindmap/server/tools"
)

func (s *Server) listMaps(w http.ResponseWriter, r *http.Request, p *principal) {
	maps, err := s.svc.Store.ListMaps(r.URL.Query().Get("workspace_id"), p.scope)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, maps)
}

func (s *Server) createMap(w http.ResponseWriter, r *http.Request, p *principal) {
	if !p.host || p.scope != "" {
		writeErr(w, contract.Errf(contract.ErrForbidden, "only an all-maps local token can create maps"))
		return
	}
	var req contract.CreateMapRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	m, _, err := s.svc.Store.CreateMap(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.svc.Hub.SetRev(m.ID, m.Rev)
	writeJSON(w, 200, m)
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request, _ *principal) {
	snap, err := s.svc.Snapshot(r.PathValue("map"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, snap)
}

func (s *Server) syncRoute(w http.ResponseWriter, r *http.Request, p *principal) {
	mapID := r.PathValue("map")
	pt, err := s.participant(p, r, mapID)
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := Sync(r.Context(), s.svc, mapID, pt, ParseSyncQuery(r.URL.Query().Get))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) submitRoute(w http.ResponseWriter, r *http.Request, p *principal) {
	mapID := r.PathValue("map")
	var req contract.SubmitRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	pt, err := s.participant(p, r, mapID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.limits.AllowN(share.LimitOps, s.limitKey(r, p), len(req.Ops)); err != nil {
		writeErr(w, err)
		return
	}
	res, err := Submit(s.svc, mapID, pt, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

// undo is a Phase 1a addition to the protocol: POST .../undo reverts the
// caller's latest batch. The inverse ops are broadcast to every client
// (including the caller) as a normal `ops` message and returned here.
func (s *Server) undo(w http.ResponseWriter, r *http.Request, p *principal) {
	mapID := r.PathValue("map")
	pt, err := s.participant(p, r, mapID)
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.svc.Undo(mapID, contract.Actor{ParticipantID: pt.ID})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, contract.OpsPayload{Ops: res.Committed, Rev: res.Rev})
}

func (s *Server) uploadAsset(w http.ResponseWriter, r *http.Request, p *principal) {
	mapID := r.PathValue("map")
	pt, err := s.participant(p, r, mapID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.limits.Allow(share.LimitAssets, s.limitKey(r, p)); err != nil {
		writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, contract.MaxAssetBytes+1<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeErr(w, contract.Errf(contract.ErrTooLarge, "multipart body: %v", err))
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, contract.FieldErr(contract.ErrBadRequest, "file", "required"))
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, contract.MaxAssetBytes+1))
	if err != nil {
		writeErr(w, contract.Errf(contract.ErrBadRequest, "read file: %v", err))
		return
	}
	nodeID := r.FormValue("node_id")
	if !contract.ValidID(contract.PrefixNode, nodeID) {
		writeErr(w, contract.FieldErr(contract.ErrBadRequest, "node_id", "required"))
		return
	}
	mime, ok := store.SniffMIME(data, hdr.Header.Get("Content-Type"))
	if !ok {
		writeErr(w, contract.FieldErr(contract.ErrUnsupportedMedia, "file", "type not allowed"))
		return
	}
	a, err := s.svc.Store.StoreBlob(data, mime)
	if err != nil {
		writeErr(w, err)
		return
	}
	a.NodeID, a.Filename, a.Caption, a.Author = nodeID, hdr.Filename, r.FormValue("caption"), contract.Actor{ParticipantID: pt.ID}
	if len(a.Filename) > 200 {
		a.Filename = a.Filename[:200]
	}
	if len(a.Caption) > contract.MaxCaptionLen {
		a.Caption = a.Caption[:contract.MaxCaptionLen]
	}
	writeJSON(w, 200, a)
}

func (s *Server) fork(w http.ResponseWriter, r *http.Request, p *principal) {
	if !p.host {
		writeErr(w, contract.Errf(contract.ErrForbidden, "forking creates a new map; host only"))
		return
	}
	var req struct {
		Rev int64 `json:"rev,omitempty"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	m, err := s.svc.Store.ForkMap(r.PathValue("map"), req.Rev)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, m)
}

func (s *Server) review(w http.ResponseWriter, r *http.Request, p *principal) {
	prop, err := s.svc.Store.GetProposal(r.PathValue("id"))
	if err != nil || !p.canAccess(prop.MapID) {
		writeErr(w, contract.Errf(contract.ErrNotFound, "proposal not found"))
		return
	}
	var req contract.ReviewProposalRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	pt, err := s.participant(p, r, prop.MapID)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.svc.Review(prop.ID, req, pt.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) profiles(w http.ResponseWriter, r *http.Request, _ *principal) {
	if s.Profiles == nil {
		writeJSON(w, 200, []contract.AgentProfile{})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ps, err := s.Profiles(ctx)
	if err != nil {
		writeErr(w, contract.Errf(contract.ErrUnavailable, "agent profiles: %v", err))
		return
	}
	writeJSON(w, 200, ps)
}

// personalToken mints a personal bridge token (mmp1.<port>.<secret>) for
// using the bridge outside Kandev. Host only; protocol §2 lists it under
// plugin settings, and this route is how the settings UI gets one.
func (s *Server) personalToken(w http.ResponseWriter, r *http.Request, p *principal) {
	if !p.host || p.scope != "" {
		writeErr(w, contract.Errf(contract.ErrForbidden, "only an all-maps local token can mint personal tokens"))
		return
	}
	var req struct {
		Label string `json:"label,omitempty"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	raw, info, err := s.svc.Store.IssueToken(contract.TokenPersonal, s.svc.Port(), "", "", "", "", req.Label, 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": info.ID, "token": raw, "label": req.Label})
}

// --- bridge -------------------------------------------------------------------------

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if v, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func (s *Server) bridgeAuth(w http.ResponseWriter, r *http.Request) (string, bool) {
	if viaTunnel(r) {
		notFound(w)
		return "", false
	}
	if v := r.Header.Get(contract.ContractHeader); v != "" && v != "1" {
		writeErr(w, contract.Errf(contract.ErrVersion, "this plugin speaks contract 1, got %s", v))
		return "", false
	}
	tok := bearer(r)
	info, err := s.svc.Store.LookupToken(tok)
	if err != nil || (info.Kind != contract.TokenRun && info.Kind != contract.TokenPersonal) {
		writeErr(w, contract.Errf(contract.ErrUnauthorized, "missing, expired or revoked bridge token"))
		return "", false
	}
	return tok, true
}

// ToolList returns the declarations from contract/tools.
func ToolList() (contract.ToolListResponse, error) {
	names := make([]string, 0, len(contract.ToolNames))
	for _, n := range contract.ToolNames {
		names = append(names, string(n))
	}
	decls, err := contractfiles.Tools[contract.ToolDeclaration](names)
	return contract.ToolListResponse{Contract: contract.ContractVersion, Tools: decls}, err
}

func (s *Server) listTools(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.bridgeAuth(w, r); !ok {
		return
	}
	l, err := ToolList()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, l)
}

const maxToolBody = 16 << 20

func (s *Server) invokeTool(w http.ResponseWriter, r *http.Request) {
	tok, ok := s.bridgeAuth(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxToolBody+1))
	if err != nil {
		writeErr(w, contract.Errf(contract.ErrBadRequest, "read body: %v", err))
		return
	}
	if len(body) > maxToolBody {
		writeErr(w, contract.Errf(contract.ErrTooLarge, "body over 16 MiB"))
		return
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		body = []byte("{}")
	}
	if !json.Valid(body) {
		writeJSON(w, 200, contract.ToolError(contract.Errf(contract.ErrBadRequest, "body must be the tool arguments as a JSON object")))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), contract.AgentToolTimeout)
	defer cancel()
	res := s.tools.Invoke(ctx, tools.Caller{Transport: contract.TransportBridge, Token: tok}, r.PathValue("name"), body)
	writeJSON(w, 200, res)
}

func (s *Server) opsRange(w http.ResponseWriter, r *http.Request, p *principal) {
	mapID := r.PathValue("map")
	if !p.canAccess(mapID) {
		writeErr(w, contract.Errf(contract.ErrNotFound, "map not found"))
		return
	}
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if from == "" || to == "" {
		writeErr(w, contract.Errf(contract.ErrBadRequest, "from and to are required"))
		return
	}

	fromRev, err := strconv.ParseInt(from, 10, 64)
	if err != nil {
		writeErr(w, contract.Errf(contract.ErrBadRequest, "from must be an integer"))
		return
	}
	toRev, err := strconv.ParseInt(to, 10, 64)
	if err != nil {
		writeErr(w, contract.Errf(contract.ErrBadRequest, "to must be an integer"))
		return
	}

	since := fromRev - 1
	if since < 0 {
		since = 0
	}
	ops, err := s.svc.Store.OpsSince(mapID, since, int(toRev-fromRev+1))
	if err == nil {
		filtered := make([]contract.Op, 0, len(ops))
		for _, op := range ops {
			if op.Rev <= toRev {
				filtered = append(filtered, op)
			}
		}
		ops = filtered
	}

	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ops": ops})
}
