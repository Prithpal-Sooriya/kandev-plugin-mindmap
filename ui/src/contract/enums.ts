// Enum values and limits. Mirrors server/contract/enums.go and limits.go;
// tests/contract.test.ts pins them to the Go-checked fixtures and schemas.

export const CONTRACT_VERSION = 1;

export const NODE_KINDS = ["topic", "idea", "research", "example", "question", "decision", "risk", "action", "doc"] as const;
export type NodeKind = (typeof NODE_KINDS)[number];

export const NODE_STATUSES = ["idle", "claimed", "running", "review", "done", "failed"] as const;
export type NodeStatus = (typeof NODE_STATUSES)[number];

export const RUN_MODES = ["apply", "propose"] as const;
export type RunMode = (typeof RUN_MODES)[number];

export const RUN_STATUSES = ["queued", "running", "waiting", "review", "done", "failed", "stopped"] as const;
export type RunStatus = (typeof RUN_STATUSES)[number];
export const TERMINAL_RUN_STATUSES: readonly RunStatus[] = ["done", "failed", "stopped"];

export const WAITING_REASONS = ["question", "turn_ended", "approval"] as const;
export type WaitingReason = (typeof WAITING_REASONS)[number];

export const TEMPLATES = ["research", "explore", "swarm", "garden", "critique", "compile", "synthesis", "execute", "custom", "expand", "examples", "suggest"] as const;
export type Template = (typeof TEMPLATES)[number];

export const LENSES = ["researcher", "skeptic", "builder", "contrarian", "examples_hunter", "futurist"] as const;
export type Lens = (typeof LENSES)[number];

export const HARNESSES = ["claude", "cursor", "pi", "codex", "other"] as const;
export type Harness = (typeof HARNESSES)[number];

export const TOOL_TRANSPORTS = ["mcp", "bridge", "prompt"] as const;
export type ToolTransport = (typeof TOOL_TRANSPORTS)[number];

export const PARTICIPANT_KINDS = ["human", "agent"] as const;
export type ParticipantKind = (typeof PARTICIPANT_KINDS)[number];

export const PROPOSAL_STATUSES = ["open", "ready", "accepted", "rejected", "partial"] as const;
export type ProposalStatus = (typeof PROPOSAL_STATUSES)[number];

export const PROPOSAL_ITEM_STATUSES = ["pending", "accepted", "rejected", "conflict"] as const;
export type ProposalItemStatus = (typeof PROPOSAL_ITEM_STATUSES)[number];

export const ASSET_KINDS = ["image", "file"] as const;
export type AssetKind = (typeof ASSET_KINDS)[number];

export const ALLOWED_ASSET_MIME = [
  "image/png", "image/jpeg", "image/gif", "image/webp", "image/svg+xml",
  "application/pdf", "text/plain", "text/markdown", "text/csv", "application/json",
] as const;

export const ACTIVITY_TYPES = [
  "nodes.added", "nodes.updated", "nodes.restructured", "link.added", "asset.attached",
  "node.claimed", "node.released", "run.started", "run.status", "run.spawned",
  "question.asked", "question.answered", "proposal.created", "proposal.resolved",
  "doc.written", "participant.joined", "participant.left", "share.started", "share.stopped",
] as const;
export type ActivityType = (typeof ACTIVITY_TYPES)[number];

export const LOCK_FIELDS = ["title", "body_md"] as const;
export type LockField = (typeof LOCK_FIELDS)[number];

export const CLEARABLE_FIELDS = ["confidence", "color", "run_id", "body_md"] as const;
export type ClearableField = (typeof CLEARABLE_FIELDS)[number];

export const OUTPUT_LINE_KINDS = ["text", "thinking", "tool", "status"] as const;
export type OutputLineKind = (typeof OUTPUT_LINE_KINDS)[number];

export const SHARE_PROVIDERS = ["cloudflared", "tailscale", "ngrok"] as const;
export const SHARE_STATUSES = ["starting", "active", "stopped", "error"] as const;

export const OP_TYPES = [
  "map.update", "node.create", "node.update", "node.move", "node.delete", "node.restore", "node.merge",
  "node.claim", "node.release", "question.answer", "link.create", "link.update", "link.delete",
  "asset.attach", "asset.detach",
] as const;
export type OpType = (typeof OP_TYPES)[number];

export const CLIENT_MSG_TYPES = ["hello", "submit", "presence.update", "output.subscribe", "output.unsubscribe", "ping"] as const;
export const SERVER_MSG_TYPES = [
  "welcome", "snapshot", "ops", "ack", "reject", "rebase", "presence", "participant",
  "run", "proposal", "activity", "output", "error", "pong",
] as const;

/** Plugin-local agent tool names, in manifest order. */
export const TOOL_NAMES = [
  "context", "read", "search", "add", "update", "attach", "restructure",
  "link", "claim", "spawn", "ask_human", "write_doc", "report",
] as const;
export type ToolName = (typeof TOOL_NAMES)[number];

export const LIMITS = {
  title: 200,
  body: 64 * 1024,
  tags: 20,
  tag: 40,
  sources: 50,
  sourceTitle: 300,
  quote: 1000,
  url: 2048,
  linkLabel: 60,
  linkReason: 500,
  caption: 300,
  question: 2000,
  summary: 8000,
  opsPerBatch: 200,
  addNodes: 100,
  addDepth: 3,
  orderKey: 64,
  displayName: 40,
  docPath: 200,
  assetBytes: 10 << 20,
  base64Chars: 1_400_000,
  longPollMaxHoldMs: 20_000,
  softLockTtlMs: 15_000,
  presenceTimeoutMs: 30_000,
} as const;

/** Token prefixes (see server/contract/tokens.go). */
export const TOKEN_KINDS = ["mml", "mms", "mmg", "mmr", "mmp"] as const;
export const GUEST_COOKIE = "mm_guest";
export const CONTRACT_HEADER = "X-Mindmap-Contract";
