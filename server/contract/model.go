package contract

import "time"

// Map is one mind map. Rev is the rev of the last committed op; a fresh map
// has rev 1 (the root node.create).
type Map struct {
	ID          string      `json:"id"`
	WorkspaceID string      `json:"workspace_id"`
	Title       string      `json:"title"`
	RootID      string      `json:"root_id"`
	RepoID      string      `json:"repo_id,omitempty"`
	Settings    MapSettings `json:"settings"`
	Rev         int64       `json:"rev"`
	// ForkedFrom is set on maps created by "fork map".
	ForkedFrom *ForkOrigin `json:"forked_from,omitempty"`
	CreatedBy  string      `json:"created_by"` // participant id
	CreatedAt  time.Time   `json:"created_at"`
	UpdatedAt  time.Time   `json:"updated_at"`
}

// ForkOrigin records the source map and rev of a fork.
type ForkOrigin struct {
	MapID string `json:"map_id"`
	Rev   int64  `json:"rev"`
}

// MapSettings are per-map knobs. Zero values mean "use the plugin default".
type MapSettings struct {
	DefaultMode      RunMode `json:"default_mode,omitempty"`
	DefaultProfileID string  `json:"default_profile_id,omitempty"` // Kandev agent profile id
	DefaultBudget    *Budget `json:"default_budget,omitempty"`
	MaxConcurrentRun int     `json:"max_concurrent_runs,omitempty"`
	ExportDir        string  `json:"export_dir,omitempty"`
	// RequireGuestRunApproval holds guest-started runs in waiting/approval
	// until the host approves. Off by default.
	RequireGuestRunApproval bool `json:"require_guest_run_approval,omitempty"`
}

// Actor attributes an op, node, link or event to a participant, and to a run
// when the participant is an agent.
type Actor struct {
	ParticipantID string `json:"participant_id"`
	RunID         string `json:"run_id,omitempty"`
}

// Node is one map node. ParentID is empty only for the root.
//
// Order is a fractional-index key: siblings sort by (order, id) ascending,
// byte-wise. Clients compute keys between neighbours; the server may rewrite a
// colliding key (reported via a WS `rebase`).
type Node struct {
	ID         string     `json:"id"`
	MapID      string     `json:"map_id"`
	ParentID   string     `json:"parent_id,omitempty"`
	Order      string     `json:"order"`
	Title      string     `json:"title"`
	BodyMD     string     `json:"body_md,omitempty"`
	Kind       NodeKind   `json:"kind"`
	Tags       []string   `json:"tags,omitempty"`
	Confidence *float64   `json:"confidence,omitempty"` // 0..1
	Sources    []Source   `json:"sources,omitempty"`
	Assets     []Asset    `json:"assets,omitempty"`
	Status     NodeStatus `json:"status"`
	RunID      string     `json:"run_id,omitempty"` // run launched from / currently working this node
	Author     Actor      `json:"author"`
	Collapsed  bool       `json:"collapsed,omitempty"`
	Color      string     `json:"color,omitempty"` // CSS colour token or #rrggbb
	// PendingProposalID is set while an open/ready proposal touches the node.
	PendingProposalID string    `json:"pending_proposal_id,omitempty"`
	Claim             *Claim    `json:"claim,omitempty"`
	Question          *Question `json:"question,omitempty"` // kind=question created by ask_human
	Doc               *DocMeta  `json:"doc,omitempty"`      // kind=doc
	Deleted           bool      `json:"deleted,omitempty"`  // tombstone; omitted from snapshots unless asked
	Rev               int64     `json:"rev"`                // rev of the last op that touched the node
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// Source is a piece of evidence behind a node.
type Source struct {
	URL        string    `json:"url"`
	Title      string    `json:"title,omitempty"`
	Quote      string    `json:"quote,omitempty"`
	AccessedAt time.Time `json:"accessed_at,omitzero"`
}

// Asset is an attachment stored in the plugin data dir. Bytes are fetched via
// the asset webhook (in Kandev) or /m/<token>/assets/<id> (guests); see
// docs/protocol.md. The contract never carries a URL for stored bytes because
// the right URL depends on the client.
type Asset struct {
	ID        string    `json:"id"`
	NodeID    string    `json:"node_id"`
	Kind      AssetKind `json:"kind"`
	MIME      string    `json:"mime"`
	Bytes     int64     `json:"bytes"`
	SHA256    string    `json:"sha256"`
	Filename  string    `json:"filename,omitempty"`
	Caption   string    `json:"caption,omitempty"`
	SourceURL string    `json:"source_url,omitempty"`
	Width     int       `json:"width,omitempty"`
	Height    int       `json:"height,omitempty"`
	Author    Actor     `json:"author"`
	CreatedAt time.Time `json:"created_at"`
}

// Claim marks a node (and its subtree, for halos) as being worked by one
// participant. Claims expire; agents renew by claiming again.
type Claim struct {
	ParticipantID string    `json:"participant_id"`
	RunID         string    `json:"run_id,omitempty"`
	Note          string    `json:"note,omitempty"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// Question is the human-in-the-loop payload of an ask_human node.
type Question struct {
	RunID      string    `json:"run_id"`
	Options    []string  `json:"options,omitempty"`
	Answer     string    `json:"answer,omitempty"`
	AnsweredBy string    `json:"answered_by,omitempty"` // participant id
	AnsweredAt time.Time `json:"answered_at,omitzero"`
}

// Open reports whether the question still waits for an answer.
func (q *Question) Open() bool { return q != nil && q.Answer == "" }

// DocMeta is the compile metadata of a kind=doc node.
type DocMeta struct {
	// Path is relative to the compile's "<slug>/" folder, e.g.
	// "pricing/index.md"; the root index is "index.md". The consolidated
	// single doc is "<slug>.md" and sits next to that folder.
	Path       string    `json:"path"`
	SourceIDs  []string  `json:"source_ids,omitempty"` // nodes this doc was compiled from
	RunID      string    `json:"run_id,omitempty"`
	CompiledAt time.Time `json:"compiled_at,omitzero"`
}

// Link is a cross-link between two nodes, outside the tree.
type Link struct {
	ID        string    `json:"id"`
	MapID     string    `json:"map_id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Label     string    `json:"label,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Author    Actor     `json:"author"`
	Deleted   bool      `json:"deleted,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Budget caps what a run (and its sub-agents) may do. Zero means "inherit the
// map default"; the server always stores a fully resolved budget on Run.
type Budget struct {
	MaxDepth     int `json:"max_depth,omitempty"`     // levels below the run's node
	MaxNodes     int `json:"max_nodes,omitempty"`     // nodes created, including by sub-agents
	MaxSubagents int `json:"max_subagents,omitempty"` // runs spawned, recursively
	MaxMinutes   int `json:"max_minutes,omitempty"`   // wall clock; enforced by stop-message
}

// BudgetUsage is what a run tree has consumed so far.
type BudgetUsage struct {
	Nodes     int `json:"nodes"`
	Subagents int `json:"subagents"`
	Depth     int `json:"depth"`
}

// Run is one agent run: a real Kandev task launched from a node.
type Run struct {
	ID            string        `json:"id"`
	MapID         string        `json:"map_id"`
	NodeID        string        `json:"node_id"`
	TaskID        string        `json:"task_id,omitempty"` // empty while queued for approval
	SessionID     string        `json:"session_id,omitempty"`
	ProfileID     string        `json:"profile_id"`
	ProfileName   string        `json:"profile_name,omitempty"`
	Harness       Harness       `json:"harness"`
	Template      Template      `json:"template"`
	Lens          Lens          `json:"lens,omitempty"`
	Mode          RunMode       `json:"mode"`
	Goal          string        `json:"goal,omitempty"`
	Budget        Budget        `json:"budget"`
	Usage         BudgetUsage   `json:"usage"`
	ParentRunID   string        `json:"parent_run_id,omitempty"`
	RootRunID     string        `json:"root_run_id,omitempty"` // top of a swarm/explorer tree; budget pool
	SwarmID       string        `json:"swarm_id,omitempty"`    // groups the lens runs of one swarm
	Status        RunStatus     `json:"status"`
	WaitingReason WaitingReason `json:"waiting_reason,omitempty"`
	Transport     ToolTransport `json:"transport"`
	ParticipantID string        `json:"participant_id"` // the agent participant
	StartedBy     string        `json:"started_by"`     // the human participant
	ProposalID    string        `json:"proposal_id,omitempty"`
	Summary       string        `json:"summary,omitempty"` // from report
	Error         string        `json:"error,omitempty"`
	LastOutput    string        `json:"last_output,omitempty"` // latest agent output line, for the dock
	Reported      bool          `json:"reported,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
	EndedAt       time.Time     `json:"ended_at,omitzero"`
}

// Proposal holds the ops of a propose-mode run until a human reviews them.
// Items are in the order the run wrote them; accepting applies the accepted
// items as live ops (with proposal_id set) in that order.
type Proposal struct {
	ID         string         `json:"id"`
	MapID      string         `json:"map_id"`
	RunID      string         `json:"run_id"`
	BaseRev    int64          `json:"base_rev"`
	Status     ProposalStatus `json:"status"`
	Summary    string         `json:"summary,omitempty"`
	Items      []ProposalItem `json:"items"`
	Author     Actor          `json:"author"`
	ResolvedBy string         `json:"resolved_by,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	ResolvedAt time.Time      `json:"resolved_at,omitzero"`
}

// ProposalItem is one proposed op plus its review state. NodeIDs lists every
// node the op touches so the UI can group "accept node by node".
type ProposalItem struct {
	Op      Op                 `json:"op"`
	Status  ProposalItemStatus `json:"status"`
	NodeIDs []string           `json:"node_ids"`
	Error   *Error             `json:"error,omitempty"` // why it is a conflict
}

// Participant is anyone in the map: the host, a guest, or an agent run.
type Participant struct {
	ID       string          `json:"id"`
	MapID    string          `json:"map_id"`
	Kind     ParticipantKind `json:"kind"`
	Name     string          `json:"name"`
	Color    string          `json:"color"`
	IsHost   bool            `json:"is_host,omitempty"`
	Harness  Harness         `json:"harness,omitempty"` // agents only
	RunID    string          `json:"run_id,omitempty"`  // agents only
	Online   bool            `json:"online,omitempty"`
	JoinedAt time.Time       `json:"joined_at"`
	LastSeen time.Time       `json:"last_seen_at"`
}

// Presence is the ephemeral, non-logged state of one participant. It is
// replaced wholesale on each update and dropped after PresenceTimeout.
type Presence struct {
	ParticipantID string    `json:"participant_id"`
	Cursor        *Point    `json:"cursor,omitempty"` // canvas coordinates
	Selection     []string  `json:"selection,omitempty"`
	Viewport      *Rect     `json:"viewport,omitempty"`
	Editing       *SoftLock `json:"editing,omitempty"`
	// FocusNodeID is where an agent last wrote; drives follow mode and
	// off-screen markers.
	FocusNodeID string    `json:"focus_node_id,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Point is a canvas coordinate.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Rect is a canvas rectangle.
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// SoftLock is "Alice is editing..." on one field of one node. It is advisory:
// the server rejects other participants' updates to the field with `locked`
// only while the lock is unexpired.
type SoftLock struct {
	NodeID    string    `json:"node_id"`
	Field     LockField `json:"field"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ActivityEvent is one line of the drawer feed, e.g.
// "Skeptic (Claude) added 3 nodes under Pricing".
type ActivityEvent struct {
	ID      string       `json:"id"`
	MapID   string       `json:"map_id"`
	Type    ActivityType `json:"type"`
	Actor   Actor        `json:"actor"`
	Summary string       `json:"summary"` // pre-rendered human text
	NodeIDs []string     `json:"node_ids,omitempty"`
	Count   int          `json:"count,omitempty"`
	Rev     int64        `json:"rev,omitempty"` // map rev after the change, if any
	TS      time.Time    `json:"ts"`
}

// DocTree is the compiled output of a branch: the doc nodes as a tree, with
// their on-disk paths. It is returned by compile and by write_doc.
type DocTree struct {
	MapID      string    `json:"map_id"`
	Slug       string    `json:"slug"`
	RootNodeID string    `json:"root_node_id"` // the branch that was compiled
	Single     *DocEntry `json:"single,omitempty"`
	Index      DocEntry  `json:"index"`
	ExportedTo string    `json:"exported_to,omitempty"` // dir or repo path, once written
}

// DocEntry is one doc node in a DocTree.
type DocEntry struct {
	NodeID   string     `json:"node_id"`
	Title    string     `json:"title"`
	Path     string     `json:"path"`
	Words    int        `json:"words,omitempty"`
	Children []DocEntry `json:"children,omitempty"`
}
