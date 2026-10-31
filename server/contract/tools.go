package contract

// Agent tool names, argument structs and results. The JSON Schemas in
// contract/tools/<name>.json are the source of truth for arguments; these
// structs mirror them exactly (contract_test checks top-level properties and
// required lists agree). Tool arguments are an agent-facing layer: they use
// `body` (markdown) instead of `body_md` and accept `@ref` wherever a node ID
// is expected.

// ToolName is the plugin-local tool name. Kandev exposes it to agents as
// kandev_kandev_plugin_mindmap_<name>; the bridge exposes it as mindmap_<name>.
type ToolName string

const (
	ToolContext     ToolName = "context"
	ToolRead        ToolName = "read"
	ToolSearch      ToolName = "search"
	ToolAdd         ToolName = "add"
	ToolUpdate      ToolName = "update"
	ToolAttach      ToolName = "attach"
	ToolRestructure ToolName = "restructure"
	ToolLink        ToolName = "link"
	ToolClaim       ToolName = "claim"
	ToolSpawn       ToolName = "spawn"
	ToolAskHuman    ToolName = "ask_human"
	ToolWriteDoc    ToolName = "write_doc"
	ToolReport      ToolName = "report"
)

// ToolNames lists the tools in manifest order.
var ToolNames = []ToolName{
	ToolContext, ToolRead, ToolSearch, ToolAdd, ToolUpdate, ToolAttach, ToolRestructure,
	ToolLink, ToolClaim, ToolSpawn, ToolAskHuman, ToolWriteDoc, ToolReport,
}

// ReadOnly reports whether the tool never writes. Read-only tools work after a
// run ends (within the token grace) and are not allowed in mindmap-patch blocks.
func (t ToolName) ReadOnly() bool {
	return t == ToolContext || t == ToolRead || t == ToolSearch
}

// PatchAllowed reports whether the tool may appear in a mindmap-patch block.
// Attach is allowed with url only (no local files or base64 from chat).
func (t ToolName) PatchAllowed() bool { return !t.ReadOnly() }

// NewToolArgs returns a pointer to the zero argument struct for t, or nil.
func NewToolArgs(t ToolName) any {
	switch t {
	case ToolContext:
		return &ContextArgs{}
	case ToolRead:
		return &ReadArgs{}
	case ToolSearch:
		return &SearchArgs{}
	case ToolAdd:
		return &AddArgs{}
	case ToolUpdate:
		return &UpdateArgs{}
	case ToolAttach:
		return &AttachArgs{}
	case ToolRestructure:
		return &RestructureArgs{}
	case ToolLink:
		return &LinkArgs{}
	case ToolClaim:
		return &ClaimArgs{}
	case ToolSpawn:
		return &SpawnArgs{}
	case ToolAskHuman:
		return &AskHumanArgs{}
	case ToolWriteDoc:
		return &WriteDocArgs{}
	case ToolReport:
		return &ReportArgs{}
	}
	return nil
}

type ContextArgs struct {
	SubtreeDepth int `json:"subtree_depth,omitempty"`
}

type ReadArgs struct {
	MapID         string `json:"map_id,omitempty"`
	NodeID        string `json:"node_id,omitempty"`
	Depth         int    `json:"depth,omitempty"`
	IncludeBodies bool   `json:"include_bodies,omitempty"`
	Cursor        string `json:"cursor,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

// SearchScope bounds a search.
type SearchScope string

const (
	ScopeMap       SearchScope = "map"
	ScopeSubtree   SearchScope = "subtree"
	ScopeWorkspace SearchScope = "workspace"
)

type SearchArgs struct {
	Query    string       `json:"query,omitempty"`
	Kinds    []NodeKind   `json:"kinds,omitempty"`
	Tags     []string     `json:"tags,omitempty"`
	Statuses []NodeStatus `json:"statuses,omitempty"`
	Scope    SearchScope  `json:"scope,omitempty"`
	NodeID   string       `json:"node_id,omitempty"`
	Limit    int          `json:"limit,omitempty"`
}

// AddNode is one node in an add batch; Children nest recursively.
type AddNode struct {
	Ref        string    `json:"ref,omitempty"`
	Title      string    `json:"title"`
	Kind       NodeKind  `json:"kind,omitempty"`
	Body       string    `json:"body,omitempty"`
	Tags       []string  `json:"tags,omitempty"`
	Confidence *float64  `json:"confidence,omitempty"`
	Sources    []Source  `json:"sources,omitempty"`
	Children   []AddNode `json:"children,omitempty"`
}

type AddArgs struct {
	ParentID string    `json:"parent_id,omitempty"`
	AfterID  string    `json:"after_id,omitempty"`
	Nodes    []AddNode `json:"nodes"`
}

type UpdateArgs struct {
	NodeID     string   `json:"node_id"`
	Title      string   `json:"title,omitempty"`
	Body       *string  `json:"body,omitempty"`
	AppendBody string   `json:"append_body,omitempty"`
	Kind       NodeKind `json:"kind,omitempty"`
	AddTags    []string `json:"add_tags,omitempty"`
	RemoveTags []string `json:"remove_tags,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
	AddSources []Source `json:"add_sources,omitempty"`
}

type AttachArgs struct {
	NodeID    string `json:"node_id"`
	URL       string `json:"url,omitempty"`
	FilePath  string `json:"file_path,omitempty"`
	Base64    string `json:"base64,omitempty"`
	MIMEType  string `json:"mime_type,omitempty"`
	Filename  string `json:"filename,omitempty"`
	Caption   string `json:"caption,omitempty"`
	SourceURL string `json:"source_url,omitempty"`
}

// RestructureKind is one step of a restructure call.
type RestructureKind string

const (
	RestructureMove    RestructureKind = "move"
	RestructureMerge   RestructureKind = "merge"
	RestructureRegroup RestructureKind = "regroup"
	RestructureRename  RestructureKind = "rename"
	RestructureRetire  RestructureKind = "retire"
)

var RestructureKinds = []RestructureKind{RestructureMove, RestructureMerge, RestructureRegroup, RestructureRename, RestructureRetire}

// RestructureOp is flat so the schema needs no oneOf; Validate checks the
// fields each kind needs.
type RestructureOp struct {
	Op       RestructureKind `json:"op"`
	NodeID   string          `json:"node_id,omitempty"`
	NodeIDs  []string        `json:"node_ids,omitempty"`
	ParentID string          `json:"parent_id,omitempty"`
	AfterID  string          `json:"after_id,omitempty"`
	IntoID   string          `json:"into_id,omitempty"`
	Title    string          `json:"title,omitempty"`
	Kind     NodeKind        `json:"kind,omitempty"`
	Ref      string          `json:"ref,omitempty"`
	Reason   string          `json:"reason,omitempty"`
}

type RestructureArgs struct {
	Reason string          `json:"reason"`
	Ops    []RestructureOp `json:"ops"`
}

type LinkArgs struct {
	FromID string `json:"from_id"`
	ToID   string `json:"to_id"`
	Label  string `json:"label"`
	Reason string `json:"reason,omitempty"`
}

// ClaimAction is claim or release.
type ClaimAction string

const (
	ClaimActionClaim   ClaimAction = "claim"
	ClaimActionRelease ClaimAction = "release"
)

type ClaimArgs struct {
	NodeID     string      `json:"node_id"`
	Action     ClaimAction `json:"action,omitempty"`
	TTLMinutes int         `json:"ttl_minutes,omitempty"`
	Note       string      `json:"note,omitempty"`
}

type SpawnAgent struct {
	NodeID   string   `json:"node_id"`
	Template Template `json:"template,omitempty"`
	Lens     Lens     `json:"lens,omitempty"`
	Goal     string   `json:"goal"`
	Profile  string   `json:"profile,omitempty"`
	Mode     RunMode  `json:"mode,omitempty"`
	Budget   *Budget  `json:"budget,omitempty"`
}

type SpawnArgs struct {
	Agents []SpawnAgent `json:"agents"`
}

type AskHumanArgs struct {
	Question string   `json:"question"`
	NodeID   string   `json:"node_id,omitempty"`
	Options  []string `json:"options,omitempty"`
	Context  string   `json:"context,omitempty"`
}

type WriteDocArgs struct {
	DocID       string   `json:"doc_id,omitempty"`
	ParentDocID string   `json:"parent_doc_id,omitempty"`
	Title       string   `json:"title"`
	Path        string   `json:"path"`
	Body        string   `json:"body"`
	SourceIDs   []string `json:"source_ids,omitempty"`
	Ref         string   `json:"ref,omitempty"`
}

// ReportStatus is the hand-off state a run asks for.
type ReportStatus string

const (
	ReportReview ReportStatus = "review"
	ReportDone   ReportStatus = "done"
)

type ReportArgs struct {
	Summary   string       `json:"summary"`
	Status    ReportStatus `json:"status"`
	NodeID    string       `json:"node_id,omitempty"`
	NextSteps []string     `json:"next_steps,omitempty"`
}

// --- results ------------------------------------------------------------------

// ToolResult is the transport-neutral result of a tool call. It maps 1:1 to
// pluginsdk.AgentToolResult and is the JSON body of POST /agent/v1/tools/<name>.
// Text is what the model reads and is never empty. On error, IsError is true,
// Text starts with "error <code>: " and Structured is {"error": Error}.
type ToolResult struct {
	Text       string         `json:"text"`
	Structured map[string]any `json:"structured,omitempty"`
	IsError    bool           `json:"is_error,omitempty"`
}

// ToolError builds the canonical error result.
func ToolError(e *Error) ToolResult {
	text := "error " + string(e.Code) + ": "
	if e.Field != "" {
		text += e.Field + ": "
	}
	return ToolResult{
		Text:       text + e.Message,
		Structured: map[string]any{"error": map[string]any{"code": string(e.Code), "message": e.Message, "field": e.Field}},
		IsError:    true,
	}
}

// WriteOutcome is the structured result of every write tool.
type WriteOutcome struct {
	// Applied is false when the run is in propose mode: the change is queued
	// in ProposalID and shows as a ghost until a human reviews it.
	Applied    bool              `json:"applied"`
	ProposalID string            `json:"proposal_id,omitempty"`
	Rev        int64             `json:"rev"`
	NodeIDs    []string          `json:"node_ids,omitempty"` // created or changed
	Refs       map[string]string `json:"refs,omitempty"`     // ref -> node id, for refs set in this call
	Budget     *BudgetLeft       `json:"budget,omitempty"`
}

// BudgetLeft is what the run tree may still spend. -1 means unlimited.
type BudgetLeft struct {
	Nodes     int `json:"nodes"`
	Subagents int `json:"subagents"`
	Depth     int `json:"depth"`
}
