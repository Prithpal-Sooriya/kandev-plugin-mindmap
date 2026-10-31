package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"

	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/web"
)

// localTokenTTL is the lifetime of an mml1 token from the session webhook.
const localTokenTTL = 12 * time.Hour

func jsonResp(status int, v any) (*pluginsdk.WebhookResponse, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return &pluginsdk.WebhookResponse{Status: 500, Body: []byte(err.Error())}, nil
	}
	return &pluginsdk.WebhookResponse{Status: int32(status), Headers: map[string]string{"Content-Type": "application/json"}, Body: b}, nil
}

func errResp(err error) (*pluginsdk.WebhookResponse, error) {
	e := contract.AsError(err)
	return jsonResp(e.Code.HTTPStatus(), contract.ErrorBody{Error: *e})
}

func header(h map[string]string, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// HandleWebhook serves the Kandev-authenticated webhooks (protocol §3).
func (p *mindmapPlugin) HandleWebhook(ctx context.Context, req *pluginsdk.WebhookRequest) (*pluginsdk.WebhookResponse, error) {
	if p.initErr != nil {
		return errResp(contract.Errf(contract.ErrUnavailable, "mind map plugin failed to start: %v", p.initErr))
	}
	q, _ := url.ParseQuery(req.Query)
	switch req.WebhookKey {
	case "session":
		return p.session(req)
	case "asset":
		return p.asset(q)
	case "sync":
		return p.sync(ctx, q)
	case "submit":
		return p.submit(q, req.Body)
	case "api":
		return p.api(ctx, req.Body)
	}
	return &pluginsdk.WebhookResponse{Status: 404}, nil
}

// session issues a local token for the in-Kandev UI.
func (p *mindmapPlugin) session(req *pluginsdk.WebhookRequest) (*pluginsdk.WebhookResponse, error) {
	var body struct {
		MapID string `json:"map_id,omitempty"`
	}
	if len(bytes.TrimSpace(req.Body)) > 0 {
		if err := contract.StrictUnmarshal(req.Body, &body); err != nil {
			return errResp(contract.Errf(contract.ErrBadRequest, "body must be {map_id?}: %v", err))
		}
	}
	for _, h := range []string{"Origin", "X-Forwarded-Origin"} {
		p.svc.AddOrigin(header(req.Headers, h))
	}
	if ref := header(req.Headers, "Referer"); ref != "" {
		if u, err := url.Parse(ref); err == nil && u.Scheme != "" && u.Host != "" {
			p.svc.AddOrigin(u.Scheme + "://" + u.Host)
		}
	}
	pt := contract.Participant{Kind: contract.ParticipantHuman, Name: "Host", Color: "#4f7cff", IsHost: true}
	if body.MapID != "" {
		host, err := p.store.HostParticipant(body.MapID)
		if err != nil {
			return errResp(err)
		}
		pt = host
	}
	raw, info, err := p.store.IssueToken(contract.TokenLocal, 0, body.MapID, "", pt.ID, "", "", localTokenTTL)
	if err != nil {
		return errResp(err)
	}
	return jsonResp(200, contract.SessionResponse{Contract: contract.ContractVersion, BaseURL: p.web.BaseURL(), Token: raw,
		MapID: body.MapID, ExpiresAt: info.ExpiresAt, Participant: pt})
}

// asset serves image bytes to the in-Kandev UI (the desktop CSP blocks
// loopback images).
func (p *mindmapPlugin) asset(q url.Values) (*pluginsdk.WebhookResponse, error) {
	a, path, err := p.store.AssetFile(q.Get("map_id"), q.Get("id"))
	if err != nil {
		return &pluginsdk.WebhookResponse{Status: 404}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return &pluginsdk.WebhookResponse{Status: 404}, nil
	}
	h := map[string]string{"Content-Type": a.MIME, "Cache-Control": "private, max-age=31536000, immutable", "X-Content-Type-Options": "nosniff"}
	if a.MIME == "image/svg+xml" {
		h["Content-Security-Policy"] = "default-src 'none'; style-src 'unsafe-inline'; sandbox"
	}
	return &pluginsdk.WebhookResponse{Status: 200, Headers: h, Body: b}, nil
}

// sync is the long-poll fallback, held at most contract.LongPollMaxHold
// (Kandev kills webhook requests at 30 s).
func (p *mindmapPlugin) sync(ctx context.Context, q url.Values) (*pluginsdk.WebhookResponse, error) {
	mapID := q.Get("map_id")
	host, err := p.store.HostParticipant(mapID)
	if err != nil {
		return errResp(err)
	}
	ctx, cancel := context.WithTimeout(ctx, contract.LongPollMaxHold+2*time.Second)
	defer cancel()
	res, err := web.Sync(ctx, p.svc, mapID, host, web.ParseSyncQuery(q.Get))
	if err != nil {
		return errResp(err)
	}
	return jsonResp(200, res)
}

func (p *mindmapPlugin) submit(q url.Values, body []byte) (*pluginsdk.WebhookResponse, error) {
	mapID := q.Get("map_id")
	var req contract.SubmitRequest
	if err := contract.StrictUnmarshal(body, &req); err != nil {
		return errResp(contract.AsError(err))
	}
	host, err := p.store.HostParticipant(mapID)
	if err != nil {
		return errResp(err)
	}
	res, err := web.Submit(p.svc, mapID, host, req)
	if err != nil {
		return errResp(err)
	}
	return jsonResp(200, res)
}

// apiRequest is the body of the `api` webhook.
type apiRequest struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// api proxies one /api/* loopback call (protocol §4) for the in-Kandev
// fallback path, acting as the host with an all-maps scope.
func (p *mindmapPlugin) api(ctx context.Context, body []byte) (*pluginsdk.WebhookResponse, error) {
	var req apiRequest
	if err := contract.StrictUnmarshal(body, &req); err != nil {
		return errResp(contract.Errf(contract.ErrBadRequest, "body must be {method, path, body?}: %v", err))
	}
	path := "/" + strings.TrimPrefix(req.Path, "/")
	if !strings.HasPrefix(path, "/api/") || strings.Contains(path, "..") {
		return errResp(contract.FieldErr(contract.ErrBadRequest, "path", "must start with /api/"))
	}
	method := strings.ToUpper(req.Method)
	if method == "" {
		method = http.MethodGet
	}
	var rb io.Reader
	if len(req.Body) > 0 {
		rb = bytes.NewReader(req.Body)
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	hr, err := http.NewRequestWithContext(web.WithInternal(ctx), method, "http://127.0.0.1/m/"+web.InternalToken+path, rb)
	if err != nil {
		return errResp(contract.Errf(contract.ErrBadRequest, "%v", err))
	}
	hr.Host = "127.0.0.1"
	hr.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.web.Handler().ServeHTTP(rec, hr)
	h := map[string]string{}
	if ct := rec.Header().Get("Content-Type"); ct != "" {
		h["Content-Type"] = ct
	}
	return &pluginsdk.WebhookResponse{Status: int32(rec.Code), Headers: h, Body: rec.Body.Bytes()}, nil
}
