package contract

import "slices"

// ContractVersion is bumped on any breaking change to a wire shape. Clients
// send it in the WS hello and the bridge sends it as X-Mindmap-Contract.
const ContractVersion = 1

// NodeKind classifies a node. It drives icon, colour and compile behaviour.
type NodeKind string

const (
	KindTopic    NodeKind = "topic"
	KindIdea     NodeKind = "idea"
	KindResearch NodeKind = "research"
	KindExample  NodeKind = "example"
	KindQuestion NodeKind = "question"
	KindDecision NodeKind = "decision"
	KindRisk     NodeKind = "risk"
	KindAction   NodeKind = "action"
	KindDoc      NodeKind = "doc"
)

// NodeKinds lists every NodeKind in display order.
var NodeKinds = []NodeKind{KindTopic, KindIdea, KindResearch, KindExample, KindQuestion, KindDecision, KindRisk, KindAction, KindDoc}

func (k NodeKind) Valid() bool { return slices.Contains(NodeKinds, k) }

// NodeStatus is the per-node work state shown as a ring on the canvas.
type NodeStatus string

const (
	StatusIdle    NodeStatus = "idle"
	StatusClaimed NodeStatus = "claimed"
	StatusRunning NodeStatus = "running"
	StatusReview  NodeStatus = "review"
	StatusDone    NodeStatus = "done"
	StatusFailed  NodeStatus = "failed"
)

var NodeStatuses = []NodeStatus{StatusIdle, StatusClaimed, StatusRunning, StatusReview, StatusDone, StatusFailed}

func (s NodeStatus) Valid() bool { return slices.Contains(NodeStatuses, s) }

// RunMode decides whether a run's writes go live or become a proposal.
type RunMode string

const (
	ModeApply   RunMode = "apply"
	ModePropose RunMode = "propose"
)

var RunModes = []RunMode{ModeApply, ModePropose}

func (m RunMode) Valid() bool { return slices.Contains(RunModes, m) }

// RunStatus is the lifecycle of a run. It is richer than NodeStatus; see
// NextRunStatus and NodeStatusForRun.
type RunStatus string

const (
	RunQueued  RunStatus = "queued"  // task created, session not running yet
	RunRunning RunStatus = "running" // agent turn in progress
	RunWaiting RunStatus = "waiting" // turn ended or ask_human open: needs a human
	RunReview  RunStatus = "review"  // reported for review, or proposal open
	RunDone    RunStatus = "done"
	RunFailed  RunStatus = "failed"
	RunStopped RunStatus = "stopped" // cancelled by a human or the kill switch
)

var RunStatuses = []RunStatus{RunQueued, RunRunning, RunWaiting, RunReview, RunDone, RunFailed, RunStopped}

func (s RunStatus) Valid() bool { return slices.Contains(RunStatuses, s) }

// Terminal reports whether the run will not write again without a new human
// message. Run tokens enter their grace period on a terminal status.
func (s RunStatus) Terminal() bool { return s == RunDone || s == RunFailed || s == RunStopped }

// WaitingReason explains RunWaiting.
type WaitingReason string

const (
	WaitingQuestion  WaitingReason = "question"   // an ask_human question is open
	WaitingTurnEnded WaitingReason = "turn_ended" // agent stopped without report
	WaitingApproval  WaitingReason = "approval"   // guest run awaiting host approval
)

var WaitingReasons = []WaitingReason{WaitingQuestion, WaitingTurnEnded, WaitingApproval}

func (r WaitingReason) Valid() bool { return slices.Contains(WaitingReasons, r) }

// Template is the run recipe. It selects the launch prompt and defaults.
type Template string

const (
	TemplateResearch  Template = "research"
	TemplateExplore   Template = "explore"
	TemplateSwarm     Template = "swarm" // one lens agent of a swarm
	TemplateGarden    Template = "garden"
	TemplateCritique  Template = "critique"
	TemplateCompile   Template = "compile"   // section agent of a compile
	TemplateSynthesis Template = "synthesis" // overview + index + consolidated doc
	TemplateExecute   Template = "execute"   // action node -> implementation task
	TemplateCustom    Template = "custom"
	TemplateExpand    Template = "expand"   // add the next level of branches
	TemplateExamples  Template = "examples" // find real-world, cited examples
	TemplateSuggest   Template = "suggest"  // one-shot utility agent: proposes child nodes (no task)
)

var Templates = []Template{TemplateResearch, TemplateExplore, TemplateSwarm, TemplateGarden, TemplateCritique, TemplateCompile, TemplateSynthesis, TemplateExecute, TemplateCustom, TemplateExpand, TemplateExamples, TemplateSuggest}

// InternalTemplates are started only through their own endpoints (compile,
// suggest), never through POST runs.
var InternalTemplates = []Template{TemplateCompile, TemplateSynthesis, TemplateSuggest}

func (t Template) Valid() bool { return slices.Contains(Templates, t) }

// SpawnTemplates are the templates the spawn tool may start.
var SpawnTemplates = []Template{TemplateResearch, TemplateExplore, TemplateSwarm, TemplateCritique, TemplateExpand, TemplateExamples}

// DefaultMode is the mode a template runs in when the caller does not choose.
// Organiser runs (garden) default to propose, per the plan.
func (t Template) DefaultMode() RunMode {
	if t == TemplateGarden {
		return ModePropose
	}
	return ModeApply
}

// Lens is the persona of a swarm agent.
type Lens string

const (
	LensResearcher     Lens = "researcher"
	LensSkeptic        Lens = "skeptic"
	LensBuilder        Lens = "builder"
	LensContrarian     Lens = "contrarian"
	LensExamplesHunter Lens = "examples_hunter"
	LensFuturist       Lens = "futurist"
)

var Lenses = []Lens{LensResearcher, LensSkeptic, LensBuilder, LensContrarian, LensExamplesHunter, LensFuturist}

func (l Lens) Valid() bool { return slices.Contains(Lenses, l) }

// Harness identifies the agent CLI behind a run, for icons. Unknown harnesses
// are reported as HarnessOther; the set is open-ended on the wire.
type Harness string

const (
	HarnessClaude Harness = "claude"
	HarnessCursor Harness = "cursor"
	HarnessPi     Harness = "pi"
	HarnessCodex  Harness = "codex"
	HarnessOther  Harness = "other"
)

var Harnesses = []Harness{HarnessClaude, HarnessCursor, HarnessPi, HarnessCodex, HarnessOther}

func (h Harness) Valid() bool { return slices.Contains(Harnesses, h) }

// ToolTransport records how a run's agent reaches the tools.
type ToolTransport string

const (
	TransportMCP    ToolTransport = "mcp"    // Kandev MCP (kandev_kandev_plugin_mindmap_<tool>)
	TransportBridge ToolTransport = "bridge" // Pi extension or stdio MCP over /agent/v1
	TransportPrompt ToolTransport = "prompt" // mindmap-patch blocks in chat
)

var ToolTransports = []ToolTransport{TransportMCP, TransportBridge, TransportPrompt}

func (t ToolTransport) Valid() bool { return slices.Contains(ToolTransports, t) }

// ParticipantKind distinguishes humans from agents.
type ParticipantKind string

const (
	ParticipantHuman ParticipantKind = "human"
	ParticipantAgent ParticipantKind = "agent"
)

var ParticipantKinds = []ParticipantKind{ParticipantHuman, ParticipantAgent}

func (k ParticipantKind) Valid() bool { return slices.Contains(ParticipantKinds, k) }

// ProposalStatus is the lifecycle of a proposal as a whole.
type ProposalStatus string

const (
	ProposalOpen     ProposalStatus = "open"     // run still writing into it
	ProposalReady    ProposalStatus = "ready"    // run reported; awaiting review
	ProposalAccepted ProposalStatus = "accepted" // every item accepted
	ProposalRejected ProposalStatus = "rejected" // every item rejected
	ProposalPartial  ProposalStatus = "partial"  // resolved with a mix
)

var ProposalStatuses = []ProposalStatus{ProposalOpen, ProposalReady, ProposalAccepted, ProposalRejected, ProposalPartial}

func (s ProposalStatus) Valid() bool { return slices.Contains(ProposalStatuses, s) }

// Resolved reports whether no more review decisions can be made.
func (s ProposalStatus) Resolved() bool {
	return s == ProposalAccepted || s == ProposalRejected || s == ProposalPartial
}

// ProposalItemStatus is the review state of one proposed op.
type ProposalItemStatus string

const (
	ItemPending  ProposalItemStatus = "pending"
	ItemAccepted ProposalItemStatus = "accepted"
	ItemRejected ProposalItemStatus = "rejected"
	ItemConflict ProposalItemStatus = "conflict" // no longer applies to the live map
)

var ProposalItemStatuses = []ProposalItemStatus{ItemPending, ItemAccepted, ItemRejected, ItemConflict}

func (s ProposalItemStatus) Valid() bool { return slices.Contains(ProposalItemStatuses, s) }

// AssetKind is the coarse type of an attachment.
type AssetKind string

const (
	AssetImage AssetKind = "image"
	AssetFile  AssetKind = "file"
)

var AssetKinds = []AssetKind{AssetImage, AssetFile}

func (k AssetKind) Valid() bool { return slices.Contains(AssetKinds, k) }

// AllowedAssetMIME lists the media types accepted for upload and attach.
var AllowedAssetMIME = []string{
	"image/png", "image/jpeg", "image/gif", "image/webp", "image/svg+xml",
	"application/pdf", "text/plain", "text/markdown", "text/csv", "application/json",
}

// AssetKindForMIME maps an allowed MIME type to its AssetKind. ok is false for
// types outside AllowedAssetMIME.
func AssetKindForMIME(mime string) (AssetKind, bool) {
	if !slices.Contains(AllowedAssetMIME, mime) {
		return "", false
	}
	if len(mime) > 6 && mime[:6] == "image/" {
		return AssetImage, true
	}
	return AssetFile, true
}

// ActivityType classifies an ActivityEvent for the drawer feed.
type ActivityType string

const (
	ActivityNodesAdded        ActivityType = "nodes.added"
	ActivityNodesUpdated      ActivityType = "nodes.updated"
	ActivityNodesRestructured ActivityType = "nodes.restructured"
	ActivityLinkAdded         ActivityType = "link.added"
	ActivityAssetAttached     ActivityType = "asset.attached"
	ActivityClaimed           ActivityType = "node.claimed"
	ActivityReleased          ActivityType = "node.released"
	ActivityRunStarted        ActivityType = "run.started"
	ActivityRunStatus         ActivityType = "run.status"
	ActivityRunSpawned        ActivityType = "run.spawned"
	ActivityQuestionAsked     ActivityType = "question.asked"
	ActivityQuestionAnswered  ActivityType = "question.answered"
	ActivityProposalCreated   ActivityType = "proposal.created"
	ActivityProposalResolved  ActivityType = "proposal.resolved"
	ActivityDocWritten        ActivityType = "doc.written"
	ActivityParticipantJoined ActivityType = "participant.joined"
	ActivityParticipantLeft   ActivityType = "participant.left"
	ActivityShareStarted      ActivityType = "share.started"
	ActivityShareStopped      ActivityType = "share.stopped"
)

var ActivityTypes = []ActivityType{
	ActivityNodesAdded, ActivityNodesUpdated, ActivityNodesRestructured, ActivityLinkAdded, ActivityAssetAttached,
	ActivityClaimed, ActivityReleased, ActivityRunStarted, ActivityRunStatus, ActivityRunSpawned,
	ActivityQuestionAsked, ActivityQuestionAnswered, ActivityProposalCreated, ActivityProposalResolved,
	ActivityDocWritten, ActivityParticipantJoined, ActivityParticipantLeft, ActivityShareStarted, ActivityShareStopped,
}

func (t ActivityType) Valid() bool { return slices.Contains(ActivityTypes, t) }

// LockField is a node field that can carry a soft lock.
type LockField string

const (
	LockTitle LockField = "title"
	LockBody  LockField = "body_md"
)

var LockFields = []LockField{LockTitle, LockBody}

func (f LockField) Valid() bool { return slices.Contains(LockFields, f) }
