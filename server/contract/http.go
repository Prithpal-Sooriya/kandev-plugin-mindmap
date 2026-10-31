package contract

import "time"

// Request and response bodies for the plugin's loopback HTTP API and the
// Kandev webhooks. Routes are listed in docs/protocol.md.

// SessionResponse is returned by the authenticated `session` webhook. The
// in-Kandev UI uses BaseURL + "/m/" + Token + "/" exactly like a guest.
type SessionResponse struct {
	Contract    int         `json:"contract"`
	BaseURL     string      `json:"base_url"` // http://127.0.0.1:<port>
	Token       string      `json:"token"`    // mml1.<secret>, scoped to MapID (or all maps if empty)
	MapID       string      `json:"map_id,omitempty"`
	ExpiresAt   time.Time   `json:"expires_at"`
	Participant Participant `json:"participant"`
}

// SyncResponse is the long-poll fallback result (GET .../sync?cursor=&wait=).
// Messages are ServerMessages in the order the WS would have sent them.
// Cursor is opaque; pass it back unchanged. If the cursor is too old the
// server returns a single `snapshot` message and a fresh cursor.
type SyncResponse struct {
	Cursor   string    `json:"cursor"`
	Messages []Message `json:"messages"`
}

// SubmitRequest is POST .../ops in the fallback; same semantics as the WS
// `submit` message. The response is a SyncResponse whose messages hold the
// ack/reject/rebase for this batch (and nothing else).
//
// Presence, when set, is the fallback's presence.update; Ops may then be empty.
type SubmitRequest struct {
	ClientID string    `json:"client_id"`
	Ops      []Op      `json:"ops"`
	Presence *Presence `json:"presence,omitempty"`
}

// MapSummary is one row of the map list.
type MapSummary struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	WorkspaceID string    `json:"workspace_id"`
	Nodes       int       `json:"nodes"`
	ActiveRuns  int       `json:"active_runs"`
	Shared      bool      `json:"shared,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CreateMapRequest creates a map with a root topic node.
type CreateMapRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Title       string `json:"title"`
	RepoID      string `json:"repo_id,omitempty"`
}

// StartRunRequest starts one run from a node (Run agent..., Explore, Garden,
// Critique, Execute).
type StartRunRequest struct {
	NodeID    string   `json:"node_id"`
	ProfileID string   `json:"profile_id,omitempty"` // default: map setting, then plugin config
	Template  Template `json:"template"`
	Lens      Lens     `json:"lens,omitempty"`
	Mode      RunMode  `json:"mode,omitempty"` // default: Template.DefaultMode()
	Goal      string   `json:"goal,omitempty"`
	Budget    *Budget  `json:"budget,omitempty"`
	RepoID    string   `json:"repo_id,omitempty"` // execute / repo-backed compile
}

// SwarmRequest starts one lens run per entry under a shared swarm id.
type SwarmRequest struct {
	NodeID string        `json:"node_id"`
	Goal   string        `json:"goal,omitempty"`
	Mode   RunMode       `json:"mode,omitempty"`
	Budget *Budget       `json:"budget,omitempty"` // per lens agent
	Agents []SwarmMember `json:"agents"`
}

type SwarmMember struct {
	Lens      Lens   `json:"lens"`
	ProfileID string `json:"profile_id,omitempty"`
}

// CompileRequest compiles a branch (or the whole map from the root).
type CompileRequest struct {
	NodeID    string `json:"node_id"`
	ProfileID string `json:"profile_id,omitempty"`
	Slug      string `json:"slug,omitempty"` // default: slugified node title
	// Target "repo" writes docs/mindmaps/<slug> on the compile task's branch;
	// "dir" writes to MapSettings.ExportDir (default ~/Kandev/mindmaps).
	Target string `json:"target,omitempty"`
}

// StartRunsResponse lists runs created by run/swarm/compile requests.
type StartRunsResponse struct {
	Runs []Run `json:"runs"`
}

// SuggestRequest asks a one-shot utility agent for child ideas.
type SuggestRequest struct {
	NodeID string `json:"node_id"`
	Count  int    `json:"count,omitempty"` // default 5, max 12
}

type SuggestResponse struct {
	Suggestions []Suggestion `json:"suggestions"`
}

type Suggestion struct {
	Title string   `json:"title"`
	Kind  NodeKind `json:"kind"`
	Body  string   `json:"body,omitempty"`
}

// ReviewProposalRequest resolves some or all items of a proposal. Item IDs are
// the proposed op IDs. AcceptAll/RejectAll apply to every pending item.
type ReviewProposalRequest struct {
	Accept    []string `json:"accept,omitempty"`
	Reject    []string `json:"reject,omitempty"`
	AcceptAll bool     `json:"accept_all,omitempty"`
	RejectAll bool     `json:"reject_all,omitempty"`
}

// MessageRunRequest sends a follow-up message to a run's agent.
type MessageRunRequest struct {
	Text string `json:"text"`
}

// AgentProfile is a Kandev agent profile as offered to map users and guests.
type AgentProfile struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Harness Harness `json:"harness"`
}

// ShareProvider names the tunnel backend.
type ShareProvider string

const (
	ShareCloudflared ShareProvider = "cloudflared"
	ShareTailscale   ShareProvider = "tailscale"
	ShareNgrok       ShareProvider = "ngrok"
)

var ShareProviders = []ShareProvider{ShareCloudflared, ShareTailscale, ShareNgrok}

// ShareStatus is the tunnel lifecycle.
type ShareStatus string

const (
	ShareStarting ShareStatus = "starting"
	ShareActive   ShareStatus = "active"
	ShareStopped  ShareStatus = "stopped"
	ShareError    ShareStatus = "error"
)

var ShareStatuses = []ShareStatus{ShareStarting, ShareActive, ShareStopped, ShareError}

// ShareLink is the host-only view of a map's sharing state. URL contains the
// secret and is only ever sent to the host participant.
type ShareLink struct {
	ID        string        `json:"id"`
	MapID     string        `json:"map_id"`
	Provider  ShareProvider `json:"provider"`
	Status    ShareStatus   `json:"status"`
	URL       string        `json:"url,omitempty"`
	Error     string        `json:"error,omitempty"`
	Install   string        `json:"install_hint,omitempty"` // e.g. "brew install cloudflared"
	CreatedAt time.Time     `json:"created_at"`
	StoppedAt time.Time     `json:"stopped_at,omitzero"`
}

// ToolListResponse is GET /agent/v1/tools: the declarations from
// contract/tools, so the bridge can register them without bundling copies.
type ToolListResponse struct {
	Contract int               `json:"contract"`
	Tools    []ToolDeclaration `json:"tools"`
}

// ToolDeclaration is the shape of contract/tools/<name>.json and of a
// manifest agent_tools entry.
type ToolDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Annotations ToolAnnotations `json:"annotations"`
	InputSchema map[string]any  `json:"input_schema"`
}

type ToolAnnotations struct {
	ReadOnlyHint    bool `json:"read_only_hint"`
	DestructiveHint bool `json:"destructive_hint"`
	IdempotentHint  bool `json:"idempotent_hint"`
	OpenWorldHint   bool `json:"open_world_hint"`
}
