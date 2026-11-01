// Package web is the plugin's loopback HTTP + WebSocket server: the guest
// and in-Kandev routes under /m/<token>/ and the bridge API under /agent/v1.
// See docs/protocol.md §1-§6.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"kandev-plugin-mindmap/server/app"
	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/share"
	"kandev-plugin-mindmap/server/store"
	"kandev-plugin-mindmap/server/tools"
)

//go:embed all:dist
var distFS embed.FS

// PortKey is the kv key holding the persisted loopback port.
const PortKey = "loopback_port"

// ProfileLister lists Kandev agent profiles (Host AgentProfiles().List).
type ProfileLister func(ctx context.Context) ([]contract.AgentProfile, error)

// RepoLister lists a workspace's Kandev repositories (Host Repositories().List).
type RepoLister func(ctx context.Context, workspaceID string) ([]contract.RepoInfo, error)

// Server is the loopback server.
type Server struct {
	svc      *app.Service
	tools    *tools.Dispatcher
	Profiles ProfileLister
	Repos    RepoLister
	links    *share.Links
	limits   *share.Limiter

	shareMu    sync.Mutex
	shareConns map[string]map[*websocket.Conn]struct{}

	mu   sync.Mutex
	http *http.Server
	ln   net.Listener
	mux  *http.ServeMux
}

// New builds the server (not listening yet).
func New(svc *app.Service, d *tools.Dispatcher) *Server {
	s := &Server{svc: svc, tools: d, limits: share.NewLimiter(share.DefaultLimits())}
	links, err := share.NewLinks(svc.Store, share.NewManager())
	if err == nil && links != nil {
		links.Port = svc.Port
		links.OnStop = s.closeShareConns
		s.links = links
	}
	s.mux = s.routes()
	return s
}

// SetShare replaces the tunnel and the rate limiter (tests).
func (s *Server) SetShare(l *share.Links, lim *share.Limiter) {
	if l != nil {
		if l.Port == nil {
			l.Port = s.svc.Port
		}
		l.OnStop = s.closeShareConns
		s.links = l
	}
	if lim != nil {
		s.limits = lim
	}
}

// Handler is the root handler (tests).
func (s *Server) Handler() http.Handler { return s.mux }

// Start listens on 127.0.0.1 at the persisted port, picking (and saving) a
// new random port when there is none or it is taken.
func (s *Server) Start() (int, error) {
	var ln net.Listener
	var err error
	if v, ok, _ := s.svc.Store.GetKV(PortKey); ok {
		if p, perr := strconv.Atoi(v); perr == nil && p > 0 {
			ln, err = net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
			if err != nil {
				ln = nil
			}
		}
	}
	if ln == nil {
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return 0, err
		}
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := s.svc.Store.SetKV(PortKey, strconv.Itoa(port)); err != nil {
		_ = ln.Close()
		return 0, err
	}
	s.svc.SetPort(port)
	srv := &http.Server{Handler: s.mux, ReadHeaderTimeout: 10 * time.Second}
	s.mu.Lock()
	s.http, s.ln = srv, ln
	s.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	return port, nil
}

// Close stops the server.
func (s *Server) Close() error {
	if s.links != nil {
		s.links.Shutdown()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.http == nil {
		return nil
	}
	return s.http.Close()
}

// BaseURL is http://127.0.0.1:<port>.
func (s *Server) BaseURL() string { return "http://127.0.0.1:" + strconv.Itoa(s.svc.Port()) }

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	m := func(pattern string, h func(w http.ResponseWriter, r *http.Request, p *principal)) {
		mux.HandleFunc(pattern, s.guard(h))
	}
	m("GET /m/{token}/ws", s.handleWS)
	m("GET /m/{token}/api/maps", s.listMaps)
	m("POST /m/{token}/api/maps", s.createMap)
	m("GET /m/{token}/api/maps/{map}/snapshot", s.snapshot)
	m("GET /m/{token}/api/maps/{map}/sync", s.syncRoute)
	m("POST /m/{token}/api/maps/{map}/ops", s.submitRoute)
	m("GET /m/{token}/api/maps/{map}/ops", s.opsRange)
	m("POST /m/{token}/api/maps/{map}/undo", s.undo)
	m("POST /m/{token}/api/maps/{map}/runs", s.startRun)
	m("POST /m/{token}/api/maps/{map}/swarms", s.startSwarm)
	m("POST /m/{token}/api/maps/{map}/nodes/{node}/answer", s.answerQuestion)
	m("POST /m/{token}/api/runs/{run}/stop", s.stopRun)
	m("POST /m/{token}/api/runs/{run}/message", s.messageRun)
	m("POST /m/{token}/api/runs/{run}/approve", s.approveRun)
	m("GET /m/{token}/api/runs/tasks", s.runTasks)
	m("GET /m/{token}/api/tasks/{task}/run", s.taskRun)
	m("GET /m/{token}/api/repos", s.repos)
	m("POST /m/{token}/api/maps/{map}/assets", s.uploadAsset)
	m("POST /m/{token}/api/maps/{map}/fork", s.fork)
	m("POST /m/{token}/api/proposals/{id}/review", s.review)
	m("GET /m/{token}/api/profiles", s.profiles)
	m("POST /m/{token}/api/tokens/personal", s.personalToken)
	m("GET /m/{token}/assets/{id}", s.assetOrStatic)
	m("POST /m/{token}/api/maps/{map}/share", s.shareMap)
	m("POST /m/{token}/api/maps/{map}/share/rotate", s.rotateShare)
	m("DELETE /m/{token}/api/maps/{map}/share", s.stopShare)
	m("GET /m/{token}/api/maps/{map}/share", s.shareStatus)
	m("POST /m/{token}/api/kill", s.killSharing)
	m("POST /m/{token}/api/join", s.joinGuest)
	m("GET /m/{token}/api/maps/{map}/audit", s.audit)
	m("GET /m/{token}/api/settings/share", s.getShareSettings)
	m("PUT /m/{token}/api/settings/share", s.putShareSettings)
	m("POST /m/{token}/api/maps/{map}/compile", s.startCompile)
	m("POST /m/{token}/api/maps/{map}/suggest", s.startSuggest)
	m("GET /m/{token}/api/maps/{map}/suggest/{job}", s.suggestJob)
	m("GET /m/{token}/api/maps/{map}/docs", s.docTrees)
	m("GET /m/{token}/api/maps/{map}/export.zip", s.exportZip)
	m("POST /m/{token}/api/maps/{map}/export", s.exportDisk)
	for _, method := range []string{"GET", "POST", "PUT", "DELETE"} {
		m(method+" /m/{token}/api/{rest...}", func(w http.ResponseWriter, _ *http.Request, _ *principal) {
			writeErr(w, contract.Errf(contract.ErrNotFound, "no such route"))
		})
	}
	m("GET /m/{token}/{path...}", s.static)
	mux.HandleFunc("OPTIONS /m/{token}/{path...}", s.guard(func(w http.ResponseWriter, _ *http.Request, _ *principal) {
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /agent/v1/tools", s.listTools)
	mux.HandleFunc("POST /agent/v1/tools/{name}", s.invokeTool)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	return mux
}

// --- principal / auth --------------------------------------------------------------

type ctxKey int

const internalKey ctxKey = 1

// InternalToken is the path token the `api` webhook uses to route a
// Kandev-authenticated request through the loopback mux as the host. It is
// honoured only when the request context carries the internal flag, which
// no network request can set.
const InternalToken = "_internal"

// WithInternal marks a request as coming from an authenticated webhook.
func WithInternal(ctx context.Context) context.Context {
	return context.WithValue(ctx, internalKey, true)
}

// principal is an authenticated /m/<token>/ caller.
type principal struct {
	kind  contract.TokenKind // mml or mms
	token store.TokenInfo
	scope string // map id, or "" for every map (local tokens only)
	host  bool
}

func (p *principal) canAccess(mapID string) bool { return p.scope == "" || p.scope == mapID }

// viaTunnel reports whether a request came through a share tunnel.
func viaTunnel(r *http.Request) bool {
	if r.Header.Get("Cf-Connecting-Ip") != "" || r.Header.Get("X-Forwarded-For") != "" {
		return true
	}
	for k := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "tailscale-") {
			return true
		}
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	return host != "127.0.0.1" && host != "localhost" && host != "::1" && host != "[::1]"
}

// notFound is the guest-route answer to every token problem: 404, empty body.
func notFound(w http.ResponseWriter) { w.WriteHeader(http.StatusNotFound) }

func (s *Server) guard(h func(w http.ResponseWriter, r *http.Request, p *principal)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := r.PathValue("token")
		var p *principal
		if raw == InternalToken {
			if v, _ := r.Context().Value(internalKey).(bool); !v {
				notFound(w)
				return
			}
			p = &principal{kind: contract.TokenLocal, host: true}
		} else {
			info, err := s.svc.Store.LookupToken(raw)
			if err != nil || (info.Kind != contract.TokenLocal && info.Kind != contract.TokenShare) {
				notFound(w)
				return
			}
			tunnel := viaTunnel(r)
			if tunnel && info.Kind != contract.TokenShare {
				notFound(w)
				return
			}
			origin := r.Header.Get("Origin")
			if info.Kind == contract.TokenLocal {
				if !s.svc.OriginAllowed(origin) {
					notFound(w)
					return
				}
				if origin != "" {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Vary", "Origin")
					w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Mindmap-Contract")
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				}
			}
			p = &principal{kind: info.Kind, token: info, scope: info.MapID, host: info.Kind == contract.TokenLocal}
		}
		if id := r.PathValue("map"); id != "" && !p.canAccess(id) {
			if p.kind == contract.TokenShare {
				notFound(w)
			} else {
				writeErr(w, contract.Errf(contract.ErrForbidden, "this token does not cover map %s", id))
			}
			return
		}
		if s.limits != nil && strings.Contains(r.URL.Path, "/api/") {
			if err := s.limits.Allow(share.LimitAPI, s.limitKey(r, p)); err != nil {
				writeErr(w, err)
				return
			}
		}
		h(w, r, p)
	}
}

// participant resolves who an HTTP call acts as. Local tokens act as the
// host. Share tokens act as the guest bound to the mm_guest cookie; a share
// call with no cookie has not joined yet.
func (s *Server) participant(p *principal, r *http.Request, mapID string) (contract.Participant, error) {
	if p.kind == contract.TokenShare {
		if pt, ok := s.guestParticipant(r, p.token); ok {
			return pt, nil
		}
		return contract.Participant{}, contract.Errf(contract.ErrBadRequest, "join the map first")
	}
	if p.token.ParticipantID != "" && p.kind != contract.TokenLocal {
		return s.svc.Store.GetParticipant(p.token.ParticipantID)
	}
	return s.svc.Store.HostParticipant(mapID)
}

// --- helpers -------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	e := contract.AsError(err)
	writeJSON(w, e.Code.HTTPStatus(), contract.ErrorBody{Error: *e})
}

const maxJSONBody = 4 << 20

func readJSON(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBody+1))
	if err != nil {
		return contract.Errf(contract.ErrBadRequest, "read body: %v", err)
	}
	if len(b) > maxJSONBody {
		return contract.Errf(contract.ErrTooLarge, "body over %d bytes", maxJSONBody)
	}
	if len(b) == 0 {
		b = []byte("{}")
	}
	if err := contract.StrictUnmarshal(b, v); err != nil {
		var ce *contract.Error
		if errors.As(err, &ce) {
			return ce
		}
		return contract.Errf(contract.ErrBadRequest, "invalid JSON: %v", err)
	}
	return nil
}

// --- static app ------------------------------------------------------------------

func (s *Server) static(w http.ResponseWriter, r *http.Request, _ *principal) {
	sub, _ := fs.Sub(distFS, "dist")
	name := strings.TrimPrefix(r.PathValue("path"), "/")
	if name == "" || strings.HasSuffix(name, "/") {
		name += "index.html"
	}
	f, err := fs.Stat(sub, name)
	if err != nil || f.IsDir() {
		name = "index.html" // SPA fallback
	}
	b, err := fs.ReadFile(sub, name)
	if err != nil {
		notFound(w)
		return
	}
	if name == "index.html" {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	w.Header().Set("Referrer-Policy", "no-referrer") // the token is in the path
	http.ServeContent(w, r, name, time.Time{}, strings.NewReader(string(b)))
}

// assetOrStatic serves stored asset bytes for as_ ids, and otherwise a file
// from the static bundle's assets/ dir (Vite's default output dir).
func (s *Server) assetOrStatic(w http.ResponseWriter, r *http.Request, p *principal) {
	id := r.PathValue("id")
	if !contract.ValidID(contract.PrefixAsset, id) {
		r.SetPathValue("path", "assets/"+id)
		s.static(w, r, p)
		return
	}
	s.serveAsset(w, r, p.scope, id, func(mapID string) bool { return p.canAccess(mapID) })
}

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, mapID, id string, allowed func(string) bool) {
	if mapID == "" {
		mapID = r.URL.Query().Get("map_id")
	}
	if mapID == "" || !allowed(mapID) {
		notFound(w)
		return
	}
	a, path, err := s.svc.Store.AssetFile(mapID, id)
	if err != nil {
		notFound(w)
		return
	}
	w.Header().Set("Content-Type", a.MIME)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if a.MIME == "image/svg+xml" {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	}
	http.ServeFile(w, r, path)
}
