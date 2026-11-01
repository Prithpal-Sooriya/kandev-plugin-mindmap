// Wire shapes, mirroring server/contract. Every Go field without omitempty is
// required here; every omitempty/omitzero field is optional. The fixture test
// (tests/contract.test.ts) validates contract/fixtures against these shapes
// and fails if any field is never exercised, exactly like the Go suite.
import {
  ACTIVITY_TYPES, ASSET_KINDS, CLEARABLE_FIELDS, HARNESSES, LENSES, LOCK_FIELDS, NODE_KINDS, NODE_STATUSES,
  OUTPUT_LINE_KINDS, PARTICIPANT_KINDS, PROPOSAL_ITEM_STATUSES, PROPOSAL_STATUSES, RUN_MODES, RUN_STATUSES,
  SHARE_PROVIDERS, SHARE_STATUSES, TEMPLATES, TOOL_NAMES, TOOL_TRANSPORTS, WAITING_REASONS,
} from "./enums";
import { ERROR_CODES } from "./errors";
import { arr, bool, int, json, num, obj, oneOf, record, str, tagged, time, type Infer, type Shape } from "./schema";

const id = str(64);

// --- model ----------------------------------------------------------------------

export const ErrorShape = obj("Error", { code: oneOf(ERROR_CODES), message: str() }, { field: str(), retry_ms: int });
export type ContractErrorBody = Infer<typeof ErrorShape>;

export const ActorShape = obj("Actor", { participant_id: id }, { run_id: id });
export type Actor = Infer<typeof ActorShape>;

export const BudgetShape = obj("Budget", {}, { max_depth: int, max_nodes: int, max_subagents: int, max_minutes: int });
export type Budget = Infer<typeof BudgetShape>;

export const BudgetUsageShape = obj("BudgetUsage", { nodes: int, subagents: int, depth: int });
export type BudgetUsage = Infer<typeof BudgetUsageShape>;

export const MapSettingsShape = obj("MapSettings", {}, {
  default_mode: oneOf(RUN_MODES),
  default_profile_id: str(),
  default_budget: BudgetShape,
  max_concurrent_runs: int,
  export_dir: str(),
  require_guest_run_approval: bool,
});
export type MapSettings = Infer<typeof MapSettingsShape>;

export const ForkOriginShape = obj("ForkOrigin", { map_id: id, rev: int });

export const MapShape = obj("Map", {
  id, workspace_id: str(), title: str(), root_id: id, settings: MapSettingsShape, rev: int,
  created_by: id, created_at: time, updated_at: time,
}, { repo_id: str(), forked_from: ForkOriginShape });
export type MindMap = Infer<typeof MapShape>;

export const SourceShape = obj("Source", { url: str() }, { title: str(), quote: str(), accessed_at: time });
export type Source = Infer<typeof SourceShape>;

export const AssetShape = obj("Asset", {
  id, node_id: id, kind: oneOf(ASSET_KINDS), mime: str(), bytes: int, sha256: str(64),
  author: ActorShape, created_at: time,
}, { filename: str(), caption: str(), source_url: str(), width: int, height: int });
export type Asset = Infer<typeof AssetShape>;

export const ClaimShape = obj("Claim", { participant_id: id, expires_at: time }, { run_id: id, note: str() });
export type Claim = Infer<typeof ClaimShape>;

export const QuestionShape = obj("Question", { run_id: id }, {
  options: arr(str()), answer: str(), answered_by: id, answered_at: time,
});
export type Question = Infer<typeof QuestionShape>;

export const DocMetaShape = obj("DocMeta", { path: str() }, { source_ids: arr(id), run_id: id, compiled_at: time });
export type DocMeta = Infer<typeof DocMetaShape>;

export const NodeShape = obj("Node", {
  id, map_id: id, order: str(), title: str(), kind: oneOf(NODE_KINDS), status: oneOf(NODE_STATUSES),
  author: ActorShape, rev: int, created_at: time, updated_at: time,
}, {
  parent_id: id, body_md: str(), tags: arr(str()), confidence: num, sources: arr(SourceShape), assets: arr(AssetShape),
  run_id: id, collapsed: bool, color: str(), pending_proposal_id: id, claim: ClaimShape, question: QuestionShape,
  doc: DocMetaShape, deleted: bool,
});
export type MapNode = Infer<typeof NodeShape>;

export const LinkShape = obj("Link", { id, map_id: id, from: id, to: id, author: ActorShape, created_at: time }, {
  label: str(), reason: str(), deleted: bool,
});
export type Link = Infer<typeof LinkShape>;

export const RunShape = obj("Run", {
  id, map_id: id, node_id: id, profile_id: str(), harness: oneOf(HARNESSES), template: oneOf(TEMPLATES),
  mode: oneOf(RUN_MODES), budget: BudgetShape, usage: BudgetUsageShape, status: oneOf(RUN_STATUSES),
  transport: oneOf(TOOL_TRANSPORTS), participant_id: id, started_by: id, created_at: time, updated_at: time,
}, {
  task_id: str(), session_id: str(), profile_name: str(), lens: oneOf(LENSES), goal: str(), parent_run_id: id,
  root_run_id: id, swarm_id: str(), waiting_reason: oneOf(WAITING_REASONS), proposal_id: id, summary: str(),
  error: str(), last_output: str(), reported: bool, ended_at: time,
});
export type Run = Infer<typeof RunShape>;

export const ParticipantShape = obj("Participant", {
  id, map_id: id, kind: oneOf(PARTICIPANT_KINDS), name: str(), color: str(), joined_at: time, last_seen_at: time,
}, { is_host: bool, harness: oneOf(HARNESSES), run_id: id, online: bool });
export type Participant = Infer<typeof ParticipantShape>;

export const PointShape = obj("Point", { x: num, y: num });
export const RectShape = obj("Rect", { x: num, y: num, w: num, h: num });
export const SoftLockShape = obj("SoftLock", { node_id: id, field: oneOf(LOCK_FIELDS), expires_at: time });
export type SoftLock = Infer<typeof SoftLockShape>;

export const PresenceShape = obj("Presence", { participant_id: id, updated_at: time }, {
  cursor: PointShape, selection: arr(id), viewport: RectShape, editing: SoftLockShape, focus_node_id: id,
});
export type Presence = Infer<typeof PresenceShape>;

export const ActivityEventShape = obj("ActivityEvent", {
  id, map_id: id, type: oneOf(ACTIVITY_TYPES), actor: ActorShape, summary: str(), ts: time,
}, { node_ids: arr(id), count: int, rev: int });
export type ActivityEvent = Infer<typeof ActivityEventShape>;

export interface DocEntry {
  node_id: string;
  title: string;
  path: string;
  words?: number;
  children?: DocEntry[];
}
export const DocEntryShape: Shape<DocEntry> = obj("DocEntry", { node_id: id, title: str(), path: str() }, {
  words: int,
  children: arr<DocEntry>({ kind: "lazy", check: (v, p, c) => DocEntryShape.check(v, p, c) }),
});

export const DocTreeShape = obj("DocTree", { map_id: id, slug: str(), root_node_id: id, index: DocEntryShape }, {
  single: DocEntryShape, exported_to: str(),
});
export type DocTree = Infer<typeof DocTreeShape>;

// --- ops ------------------------------------------------------------------------

export const NodeInitShape = obj("NodeInit", { id, order: str(), title: str(), kind: oneOf(NODE_KINDS) }, {
  parent_id: id, body_md: str(), tags: arr(str()), confidence: num, sources: arr(SourceShape),
  status: oneOf(NODE_STATUSES), color: str(), collapsed: bool, question: QuestionShape, doc: DocMetaShape,
});
export type NodeInit = Infer<typeof NodeInitShape>;

export const NodeFieldsShape = obj("NodeFields", {}, {
  title: str(), body_md: str(), kind: oneOf(NODE_KINDS), tags: arr(str()), confidence: num,
  status: oneOf(NODE_STATUSES), run_id: str(), color: str(), collapsed: bool, doc: DocMetaShape,
});
export type NodeFields = Infer<typeof NodeFieldsShape>;

export const LinkInitShape = obj("LinkInit", { id, from: id, to: id }, { label: str(), reason: str() });

export const OP_PAYLOADS = {
  "map.update": obj("MapUpdatePayload", {}, { title: str(), repo_id: str(), settings: MapSettingsShape }),
  "node.create": obj("NodeCreatePayload", { node: NodeInitShape }),
  "node.update": obj("NodeUpdatePayload", { id }, {
    set: NodeFieldsShape, clear: arr(oneOf(CLEARABLE_FIELDS)), append_body: str(), add_tags: arr(str()),
    remove_tags: arr(str()), add_sources: arr(SourceShape),
  }),
  "node.move": obj("NodeMovePayload", { id, parent_id: id, order: str() }),
  "node.delete": obj("NodeDeletePayload", { id }),
  "node.restore": obj("NodeRestorePayload", { id }),
  "node.merge": obj("NodeMergePayload", { into_id: id, ids: arr(id) }),
  "node.claim": obj("NodeClaimPayload", { id, claim: ClaimShape }),
  "node.release": obj("NodeReleasePayload", { id }),
  "question.answer": obj("QuestionAnswerPayload", { node_id: id, answer: str() }),
  "link.create": obj("LinkCreatePayload", { link: LinkInitShape }),
  "link.update": obj("LinkUpdatePayload", { id }, { label: str(), reason: str() }),
  "link.delete": obj("LinkDeletePayload", { id }),
  "asset.attach": obj("AssetAttachPayload", { asset: AssetShape }),
  "asset.detach": obj("AssetDetachPayload", { id, node_id: id }),
};

/** One entry of the op log; `payload` is typed by `type`. */
export const OpShape = tagged("Op", OP_PAYLOADS, { id, map_id: id, rev: int, actor: ActorShape }, {
  proposal_id: id, batch_id: id, base_rev: int, ts: time,
});
export type Op = Infer<typeof OpShape>;
export type OpPayload<T extends Op["type"]> = Extract<Op, { type: T }>["payload"];

export const ProposalItemShape = obj("ProposalItem", { op: OpShape, status: oneOf(PROPOSAL_ITEM_STATUSES), node_ids: arr(id) }, {
  error: ErrorShape,
});
export type ProposalItem = Infer<typeof ProposalItemShape>;

export const ProposalShape = obj("Proposal", {
  id, map_id: id, run_id: id, base_rev: int, status: oneOf(PROPOSAL_STATUSES), items: arr(ProposalItemShape),
  author: ActorShape, created_at: time,
}, { summary: str(), resolved_by: id, resolved_at: time });
export type Proposal = Infer<typeof ProposalShape>;

// --- WebSocket / long-poll messages -------------------------------------------------

export const SnapshotShape = obj("Snapshot", {
  rev: int, map: MapShape, nodes: arr(NodeShape), links: arr(LinkShape), runs: arr(RunShape),
  proposals: arr(ProposalShape), participants: arr(ParticipantShape), presence: arr(PresenceShape),
  activity: arr(ActivityEventShape),
});
export type Snapshot = Infer<typeof SnapshotShape>;

const PingPayload = obj("PingPayload", {}, { nonce: str() });
const OutputSubscription = obj("OutputSubscriptionPayload", { run_id: id });
export const OutputLineShape = obj("OutputLine", { ts: time, kind: oneOf(OUTPUT_LINE_KINDS), text: str() });
export type OutputLine = Infer<typeof OutputLineShape>;

export const CLIENT_PAYLOADS = {
  hello: obj("HelloPayload", { contract: int, client_id: str(100) }, {
    participant_id: id, name: str(40), color: str(32), since_rev: int,
  }),
  submit: obj("SubmitPayload", { ops: arr(OpShape) }),
  "presence.update": obj("PresenceUpdatePayload", { presence: PresenceShape }),
  "output.subscribe": OutputSubscription,
  "output.unsubscribe": OutputSubscription,
  ping: PingPayload,
};

export const SERVER_PAYLOADS = {
  welcome: obj("WelcomePayload", { contract: int, participant: ParticipantShape, server_time: time }, { cursor: str() }),
  snapshot: SnapshotShape,
  ops: obj("OpsPayload", { ops: arr(OpShape), rev: int }),
  ack: obj("AckPayload", { acks: arr(obj("OpAck", { id, rev: int })) }),
  reject: obj("RejectPayload", { ids: arr(id), error: ErrorShape }),
  rebase: obj("RebasePayload", { op: OpShape }),
  presence: obj("PresencePayload", {}, { presence: arr(PresenceShape), left: arr(id) }),
  participant: obj("ParticipantPayload", { participant: ParticipantShape }),
  run: obj("RunPayload", { run: RunShape }),
  proposal: obj("ProposalPayload", { proposal: ProposalShape }),
  activity: obj("ActivityPayload", { events: arr(ActivityEventShape) }),
  output: obj("OutputPayload", { run_id: id, lines: arr(OutputLineShape) }),
  error: obj("ErrorPayload", { error: ErrorShape }, { fatal: bool }),
  pong: PingPayload,
};

export const ClientMessageShape = tagged("ClientMessage", CLIENT_PAYLOADS, {});
export type ClientMessage = Infer<typeof ClientMessageShape>;
export const ServerMessageShape = tagged("ServerMessage", SERVER_PAYLOADS, {});
export type ServerMessage = Infer<typeof ServerMessageShape>;

// --- HTTP bodies ------------------------------------------------------------------

export const ErrorBodyShape = obj("ErrorBody", { error: ErrorShape });

export const SessionResponseShape = obj("SessionResponse", {
  contract: int, base_url: str(), token: str(), expires_at: time, participant: ParticipantShape,
}, { map_id: id });
export type SessionResponse = Infer<typeof SessionResponseShape>;

export const SyncResponseShape = obj("SyncResponse", { cursor: str(), messages: arr(ServerMessageShape) });
export type SyncResponse = Infer<typeof SyncResponseShape>;

export const SubmitRequestShape = obj("SubmitRequest", { client_id: str(100), ops: arr(OpShape) }, { presence: PresenceShape });
export type SubmitRequest = Infer<typeof SubmitRequestShape>;

export const MapSummaryShape = obj("MapSummary", {
  id, title: str(), workspace_id: str(), nodes: int, active_runs: int, updated_at: time,
}, { shared: bool });
export type MapSummary = Infer<typeof MapSummaryShape>;

export const CreateMapRequestShape = obj("CreateMapRequest", { workspace_id: str(), title: str() }, { repo_id: str() });

export const StartRunRequestShape = obj("StartRunRequest", { node_id: id, template: oneOf(TEMPLATES) }, {
  profile_id: str(), lens: oneOf(LENSES), mode: oneOf(RUN_MODES), goal: str(), budget: BudgetShape, repo_id: str(),
});
export type StartRunRequest = Infer<typeof StartRunRequestShape>;

export const SwarmRequestShape = obj("SwarmRequest", {
  node_id: id, agents: arr(obj("SwarmMember", { lens: oneOf(LENSES) }, { profile_id: str() })),
}, { goal: str(), mode: oneOf(RUN_MODES), budget: BudgetShape });
export type SwarmRequest = Infer<typeof SwarmRequestShape>;

export const CompileRequestShape = obj("CompileRequest", { node_id: id }, { profile_id: str(), slug: str(), target: oneOf(["repo", "dir"]) });
export type CompileRequest = Infer<typeof CompileRequestShape>;

export const StartRunsResponseShape = obj("StartRunsResponse", { runs: arr(RunShape) });
export const SuggestRequestShape = obj("SuggestRequest", { node_id: id }, { count: int });
export const SuggestResponseShape = obj("SuggestResponse", {
  suggestions: arr(obj("Suggestion", { title: str(), kind: oneOf(NODE_KINDS) }, { body: str() })),
});
export const ReviewProposalRequestShape = obj("ReviewProposalRequest", {}, {
  accept: arr(id), reject: arr(id), accept_all: bool, reject_all: bool,
});
export type ReviewProposalRequest = Infer<typeof ReviewProposalRequestShape>;
export const MessageRunRequestShape = obj("MessageRunRequest", { text: str() });
export const AgentProfileShape = obj("AgentProfile", { id: str(), name: str(), harness: oneOf(HARNESSES) });
export type AgentProfile = Infer<typeof AgentProfileShape>;
export const AnswerQuestionRequestShape = obj("AnswerQuestionRequest", { answer: str() });
export type AnswerQuestionRequest = Infer<typeof AnswerQuestionRequestShape>;
export const RepoInfoShape = obj("RepoInfo", { id: str(), name: str() });
export type RepoInfo = Infer<typeof RepoInfoShape>;
export const RunTasksResponseShape = obj("RunTasksResponse", { task_ids: arr(str()) });
export type RunTasksResponse = Infer<typeof RunTasksResponseShape>;
export const NodeRefShape = obj("NodeRef", { id, title: str(), kind: oneOf(NODE_KINDS), status: oneOf(NODE_STATUSES) }, { depth: int });
export type NodeRef = Infer<typeof NodeRefShape>;
export const TaskRunInfoShape = obj("TaskRunInfo", { run: RunShape, map_title: str(), path: arr(NodeRefShape), branch: arr(NodeRefShape) }, { more: bool });
export type TaskRunInfo = Infer<typeof TaskRunInfoShape>;

export const ShareSettingsShape = obj("ShareSettings", {}, {
  provider: oneOf(SHARE_PROVIDERS),
  max_concurrent_runs: int,
  max_nodes: int,
  max_depth: int,
  max_subagents: int,
  max_minutes: int,
  require_guest_run_approval: bool,
});
export type ShareSettings = Infer<typeof ShareSettingsShape>;

export const ShareLinkShape = obj("ShareLink", {
  id, map_id: id, provider: oneOf(SHARE_PROVIDERS), status: oneOf(SHARE_STATUSES), created_at: time,
}, { url: str(), error: str(), install_hint: str(), stopped_at: time });
export type ShareLink = Infer<typeof ShareLinkShape>;

// --- agent tools ------------------------------------------------------------------

export const ToolResultShape = obj("ToolResult", { text: str() }, { structured: record(json), is_error: bool });
export type ToolResult = Infer<typeof ToolResultShape>;

export const BudgetLeftShape = obj("BudgetLeft", { nodes: int, subagents: int, depth: int });
export const WriteOutcomeShape = obj("WriteOutcome", { applied: bool, rev: int }, {
  proposal_id: id, node_ids: arr(id), refs: record(str()), budget: BudgetLeftShape,
});
export type WriteOutcome = Infer<typeof WriteOutcomeShape>;

export const PatchShape = obj("Patch", { v: int, calls: arr(obj("PatchCall", { tool: oneOf(TOOL_NAMES), args: json })) });
export type Patch = Infer<typeof PatchShape>;

/** contract/fixtures/<Name>.json -> shape. Must match fixtureTypes in Go. */
export const FIXTURE_SHAPES: Record<string, Shape<unknown>> = {
  Map: MapShape,
  Node: NodeShape,
  Link: LinkShape,
  Run: RunShape,
  Op: OpShape,
  Proposal: ProposalShape,
  Participant: ParticipantShape,
  Presence: PresenceShape,
  ActivityEvent: ActivityEventShape,
  DocTree: DocTreeShape,
  ClientMessage: ClientMessageShape,
  ServerMessage: ServerMessageShape,
  Error: ErrorShape,
  ErrorBody: ErrorBodyShape,
  ToolResult: ToolResultShape,
  WriteOutcome: WriteOutcomeShape,
  SessionResponse: SessionResponseShape,
  SyncResponse: SyncResponseShape,
  SubmitRequest: SubmitRequestShape,
  MapSummary: MapSummaryShape,
  CreateMapRequest: CreateMapRequestShape,
  StartRunRequest: StartRunRequestShape,
  SwarmRequest: SwarmRequestShape,
  CompileRequest: CompileRequestShape,
  StartRunsResponse: StartRunsResponseShape,
  SuggestRequest: SuggestRequestShape,
  SuggestResponse: SuggestResponseShape,
  ReviewProposalRequest: ReviewProposalRequestShape,
  MessageRunRequest: MessageRunRequestShape,
  AgentProfile: AgentProfileShape,
  AnswerQuestionRequest: AnswerQuestionRequestShape,
  RepoInfo: RepoInfoShape,
  RunTasksResponse: RunTasksResponseShape,
  TaskRunInfo: TaskRunInfoShape,
  ShareLink: ShareLinkShape,
  ShareSettings: ShareSettingsShape,
  Patch: PatchShape,
};
