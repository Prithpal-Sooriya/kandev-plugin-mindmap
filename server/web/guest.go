package web

import (
	"net/http"

	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/store"
)

// cookieSecure is true when the guest page was loaded through the tunnel
// (or any TLS hop). Loopback http stays unset so tests and the host can
// round-trip the cookie.
func cookieSecure(r *http.Request) bool {
	if r.TLS != nil || viaTunnel(r) {
		return true
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}

func (s *Server) setGuestCookie(w http.ResponseWriter, r *http.Request, shareRaw, guestRaw string) {
	http.SetCookie(w, &http.Cookie{
		Name:     contract.GuestCookieName,
		Value:    guestRaw,
		Path:     "/m/" + shareRaw + "/",
		HttpOnly: true,
		Secure:   cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(contract.GuestIdleTTL.Seconds()),
	})
}

// guestParticipant returns the participant bound to the mm_guest cookie when
// that guest token belongs to this share link. The cookie is the session:
// a participant id from the client is not trusted, because those ids are
// broadcast to everyone on the map.
func (s *Server) guestParticipant(r *http.Request, shareTok store.TokenInfo) (contract.Participant, bool) {
	info, ok := s.guestToken(r, shareTok)
	if !ok {
		return contract.Participant{}, false
	}
	pt, err := s.svc.Store.GetParticipant(info.ParticipantID)
	if err != nil || pt.MapID != shareTok.MapID || pt.IsHost || pt.Kind != contract.ParticipantHuman {
		return contract.Participant{}, false
	}
	_ = s.svc.Store.TouchToken(info.ID, contract.GuestIdleTTL)
	_ = s.svc.Store.TouchParticipant(pt.ID)
	return pt, true
}

func (s *Server) guestToken(r *http.Request, shareTok store.TokenInfo) (store.TokenInfo, bool) {
	c, err := r.Cookie(contract.GuestCookieName)
	if err != nil || c.Value == "" || shareTok.ShareLinkID == "" {
		return store.TokenInfo{}, false
	}
	info, err := s.svc.Store.LookupToken(c.Value)
	if err != nil || info.Kind != contract.TokenGuest {
		return store.TokenInfo{}, false
	}
	if info.ShareLinkID != shareTok.ShareLinkID || info.MapID != shareTok.MapID || info.ParticipantID == "" {
		return store.TokenInfo{}, false
	}
	return info, true
}

// limitKey identifies the token bucket. Empty means "do not limit" (the
// internal webhook principal, which Kandev already authenticated).
func (s *Server) limitKey(r *http.Request, p *principal) string {
	if p == nil || p.token.ID == "" || s.limits == nil {
		return ""
	}
	if p.kind == contract.TokenShare {
		if info, ok := s.guestToken(r, p.token); ok {
			return "guest:" + info.ID
		}
		return "share:" + p.token.ID
	}
	return "local:" + p.token.ID
}

// runLimitKey keys run starts by the share link, not the guest cookie.
// Joining again mints a new guest token; the run bucket stays put.
func (s *Server) runLimitKey(r *http.Request, p *principal) string {
	if p != nil && p.kind == contract.TokenShare && p.token.ShareLinkID != "" {
		return "runs:" + p.token.ShareLinkID
	}
	return s.limitKey(r, p)
}
