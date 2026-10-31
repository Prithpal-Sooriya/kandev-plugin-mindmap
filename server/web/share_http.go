package web

import (
	"net/http"
	"strings"

	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/share"
)

func hostOnly(w http.ResponseWriter, p *principal) bool {
	if p.host && p.kind == contract.TokenLocal {
		return true
	}
	writeErr(w, contract.Errf(contract.ErrForbidden, "host only"))
	return false
}

func (s *Server) shareReady(w http.ResponseWriter) bool {
	if s.links == nil {
		writeErr(w, contract.Errf(contract.ErrUnavailable, "sharing is not available"))
		return false
	}
	return true
}

func (s *Server) hostActor(p *principal, mapID string) string {
	if p.token.ParticipantID != "" {
		return p.token.ParticipantID
	}
	if h, err := s.svc.Store.HostParticipant(mapID); err == nil {
		return h.ID
	}
	return ""
}

func (s *Server) shareMap(w http.ResponseWriter, r *http.Request, p *principal) {
	if !hostOnly(w, p) || !s.shareReady(w) {
		return
	}
	mapID := r.PathValue("map")
	link, err := s.links.Share(r.Context(), mapID, s.hostActor(p, mapID))
	if err != nil {
		writeErr(w, err)
		return
	}
	s.svc.Activity(contract.ActivityEvent{MapID: mapID, Type: contract.ActivityShareStarted, Actor: contract.Actor{ParticipantID: s.hostActor(p, mapID)},
		Summary: "Started sharing"})
	writeJSON(w, http.StatusOK, link)
}

func (s *Server) rotateShare(w http.ResponseWriter, r *http.Request, p *principal) {
	if !hostOnly(w, p) || !s.shareReady(w) {
		return
	}
	mapID := r.PathValue("map")
	link, err := s.links.Rotate(r.Context(), mapID, s.hostActor(p, mapID))
	if err != nil {
		writeErr(w, err)
		return
	}
	s.svc.Activity(contract.ActivityEvent{MapID: mapID, Type: contract.ActivityShareStarted, Actor: contract.Actor{ParticipantID: s.hostActor(p, mapID)},
		Summary: "Rotated the share link"})
	writeJSON(w, http.StatusOK, link)
}

func (s *Server) stopShare(w http.ResponseWriter, r *http.Request, p *principal) {
	if !hostOnly(w, p) || !s.shareReady(w) {
		return
	}
	mapID := r.PathValue("map")
	_, was, _ := s.links.Status(mapID)
	if err := s.links.Stop(mapID); err != nil {
		writeErr(w, err)
		return
	}
	if was {
		s.svc.Activity(contract.ActivityEvent{MapID: mapID, Type: contract.ActivityShareStopped, Actor: contract.Actor{ParticipantID: s.hostActor(p, mapID)},
			Summary: "Stopped sharing"})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) shareStatus(w http.ResponseWriter, r *http.Request, p *principal) {
	if !hostOnly(w, p) || !s.shareReady(w) {
		return
	}
	link, ok, err := s.links.Status(r.PathValue("map"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeErr(w, contract.Errf(contract.ErrNotFound, "this map is not shared"))
		return
	}
	writeJSON(w, http.StatusOK, link)
}

func (s *Server) killSharing(w http.ResponseWriter, r *http.Request, p *principal) {
	if !hostOnly(w, p) || !s.shareReady(w) {
		return
	}
	rows, _ := s.svc.Store.ListLiveShares()
	if err := s.links.Kill(); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.svc.StopAllRuns(r.Context()); err != nil {
		writeErr(w, err)
		return
	}
	for _, row := range rows {
		s.svc.Activity(contract.ActivityEvent{MapID: row.MapID, Type: contract.ActivityShareStopped, Actor: contract.Actor{ParticipantID: s.hostActor(p, row.MapID)},
			Summary: "Kill switch stopped sharing"})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) joinGuest(w http.ResponseWriter, r *http.Request, p *principal) {
	if p.kind != contract.TokenShare {
		writeErr(w, contract.Errf(contract.ErrForbidden, "join is for share links"))
		return
	}
	if p.token.ShareLinkID == "" {
		writeErr(w, contract.Errf(contract.ErrBadRequest, "this link is not an active share"))
		return
	}
	shareRaw := r.PathValue("token")
	if pt, ok := s.guestParticipant(r, p.token); ok {
		if c, err := r.Cookie(contract.GuestCookieName); err == nil {
			s.setGuestCookie(w, r, shareRaw, c.Value)
		}
		writeJSON(w, http.StatusOK, pt)
		return
	}
	var body struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		writeErr(w, contract.FieldErr(contract.ErrBadRequest, "name", "required"))
		return
	}
	if s.limits != nil {
		if err := s.limits.Allow(share.LimitJoins, "joins:"+p.token.ShareLinkID); err != nil {
			writeErr(w, err)
			return
		}
	}
	pt, err := s.svc.Store.CreateGuest(p.token.MapID, body.Name, body.Color)
	if err != nil {
		writeErr(w, err)
		return
	}
	raw, _, err := s.svc.Store.IssueToken(contract.TokenGuest, 0, p.token.MapID, "", pt.ID, p.token.ShareLinkID, "", contract.GuestIdleTTL)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.setGuestCookie(w, r, shareRaw, raw)
	s.svc.Hub.Publish(p.token.MapID, contract.Message{Type: contract.MsgParticipant, Payload: &contract.ParticipantPayload{Participant: pt}})
	s.svc.Activity(contract.ActivityEvent{MapID: p.token.MapID, Type: contract.ActivityParticipantJoined, Actor: contract.Actor{ParticipantID: pt.ID},
		Summary: pt.Name + " joined"})
	writeJSON(w, http.StatusOK, pt)
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request, p *principal) {
	ev, err := s.svc.Store.Audit(r.PathValue("map"), 200)
	if err != nil {
		writeErr(w, err)
		return
	}
	if ev == nil {
		ev = []contract.ActivityEvent{}
	}
	writeJSON(w, http.StatusOK, ev)
	_ = p
}

func (s *Server) getShareSettings(w http.ResponseWriter, _ *http.Request, p *principal) {
	if !hostOnly(w, p) {
		return
	}
	writeJSON(w, http.StatusOK, share.LoadSettings(s.svc.Store))
}

func (s *Server) putShareSettings(w http.ResponseWriter, r *http.Request, p *principal) {
	if !hostOnly(w, p) {
		return
	}
	var in contract.ShareSettings
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if err := share.SaveSettings(s.svc.Store, in); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, share.LoadSettings(s.svc.Store))
}
