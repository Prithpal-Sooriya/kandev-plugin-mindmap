-- Mind Map plugin storage, migration v1.
--
-- SQLite via modernc.org/sqlite (pure Go). Open with:
--   PRAGMA journal_mode = WAL; PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000;
-- Apply this file in one transaction when PRAGMA user_version = 0, then set
-- PRAGMA user_version = 1.
--
-- Conventions:
--   * IDs are contract IDs (map_..., nd_..., ...). TEXT PRIMARY KEY.
--   * Timestamps are RFC 3339 UTC TEXT, matching the wire format.
--   * *_json columns hold the contract JSON of that field and are read and
--     written whole. Never query inside them; add a column instead.
--   * Rows are never hard-deleted by normal editing: nodes and links are
--     tombstoned (deleted = 1) so undo, replay and proposals keep working.
--   * Tokens are stored only as sha256(raw token) hex.

-- One row per map. rev is the rev of the last committed op.
CREATE TABLE maps (
  id            TEXT PRIMARY KEY,
  workspace_id  TEXT NOT NULL,
  title         TEXT NOT NULL,
  root_id       TEXT NOT NULL,
  repo_id       TEXT,
  settings_json TEXT NOT NULL DEFAULT '{}',          -- contract.MapSettings
  rev           INTEGER NOT NULL DEFAULT 0,
  forked_from_map_id TEXT,
  forked_from_rev    INTEGER,
  created_by    TEXT NOT NULL,                        -- participant id
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  deleted_at    TEXT                                   -- map deletion is soft for 30 days
) STRICT;

CREATE INDEX maps_workspace ON maps (workspace_id, updated_at DESC) WHERE deleted_at IS NULL;

-- Current (materialised) node state. The op log is the history.
CREATE TABLE nodes (
  id            TEXT PRIMARY KEY,
  map_id        TEXT NOT NULL REFERENCES maps (id) ON DELETE CASCADE,
  parent_id     TEXT,                                  -- NULL only for the root
  ord           TEXT NOT NULL,                         -- fractional order key
  title         TEXT NOT NULL,
  body_md       TEXT NOT NULL DEFAULT '',
  kind          TEXT NOT NULL CHECK (kind IN ('topic','idea','research','example','question','decision','risk','action','doc')),
  tags_json     TEXT NOT NULL DEFAULT '[]',            -- []string
  confidence    REAL CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
  sources_json  TEXT NOT NULL DEFAULT '[]',            -- []contract.Source
  source_count  INTEGER NOT NULL DEFAULT 0,            -- denormalised for outlines and evidence styling
  status        TEXT NOT NULL DEFAULT 'idle' CHECK (status IN ('idle','claimed','running','review','done','failed')),
  run_id        TEXT,
  author_participant_id TEXT NOT NULL,
  author_run_id TEXT,
  collapsed     INTEGER NOT NULL DEFAULT 0,
  color         TEXT,
  pending_proposal_id TEXT,
  claim_participant_id TEXT,
  claim_run_id  TEXT,
  claim_note    TEXT,
  claim_expires_at TEXT,
  question_json TEXT,                                  -- contract.Question, kind = question
  doc_json      TEXT,                                  -- contract.DocMeta, kind = doc
  deleted       INTEGER NOT NULL DEFAULT 0,
  rev           INTEGER NOT NULL,                      -- rev of the last op touching this node
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
) STRICT;

CREATE INDEX nodes_children ON nodes (map_id, parent_id, ord, id) WHERE deleted = 0;
CREATE INDEX nodes_map_rev ON nodes (map_id, rev);
CREATE INDEX nodes_run ON nodes (run_id) WHERE run_id IS NOT NULL;
CREATE INDEX nodes_status ON nodes (map_id, status) WHERE deleted = 0;
CREATE INDEX nodes_kind ON nodes (map_id, kind) WHERE deleted = 0;
CREATE INDEX nodes_claims ON nodes (map_id, claim_expires_at) WHERE claim_participant_id IS NOT NULL;

-- Full-text search over titles, bodies, tags and source titles (search tool).
-- Contentless-delete FTS keyed by nodes.rowid; the store keeps it in sync.
CREATE VIRTUAL TABLE nodes_fts USING fts5 (
  title, body_md, tags, source_titles,
  content = '', contentless_delete = 1,
  tokenize = 'porter unicode61'
);

CREATE TABLE links (
  id            TEXT PRIMARY KEY,
  map_id        TEXT NOT NULL REFERENCES maps (id) ON DELETE CASCADE,
  from_id       TEXT NOT NULL,
  to_id         TEXT NOT NULL,
  label         TEXT NOT NULL DEFAULT '',
  reason        TEXT NOT NULL DEFAULT '',
  author_participant_id TEXT NOT NULL,
  author_run_id TEXT,
  deleted       INTEGER NOT NULL DEFAULT 0,
  rev           INTEGER NOT NULL,
  created_at    TEXT NOT NULL,
  CHECK (from_id <> to_id)
) STRICT;

CREATE INDEX links_map ON links (map_id) WHERE deleted = 0;
CREATE INDEX links_from ON links (from_id) WHERE deleted = 0;
CREATE INDEX links_to ON links (to_id) WHERE deleted = 0;

-- Append-only op log. (map_id, rev) is dense: 1, 2, 3, ... with no gaps.
CREATE TABLE ops (
  map_id        TEXT NOT NULL REFERENCES maps (id) ON DELETE CASCADE,
  rev           INTEGER NOT NULL,
  id            TEXT NOT NULL,                         -- client- or server-minted op id
  type          TEXT NOT NULL,
  payload_json  TEXT NOT NULL,                         -- contract op payload
  inverse_json  TEXT NOT NULL,                         -- full inverse Op JSON, for per-author undo
  actor_participant_id TEXT NOT NULL,
  actor_run_id  TEXT,
  proposal_id   TEXT,
  batch_id      TEXT,
  base_rev      INTEGER,
  ts            TEXT NOT NULL,
  PRIMARY KEY (map_id, rev)
) STRICT, WITHOUT ROWID;

-- Op ids are unique per map; resubmission after reconnect is acked, not re-applied.
CREATE UNIQUE INDEX ops_id ON ops (map_id, id);
CREATE INDEX ops_actor ON ops (map_id, actor_participant_id, rev DESC);
CREATE INDEX ops_run ON ops (actor_run_id, rev) WHERE actor_run_id IS NOT NULL;

CREATE TABLE participants (
  id            TEXT PRIMARY KEY,
  map_id        TEXT NOT NULL REFERENCES maps (id) ON DELETE CASCADE,
  kind          TEXT NOT NULL CHECK (kind IN ('human','agent')),
  name          TEXT NOT NULL,
  color         TEXT NOT NULL,
  is_host       INTEGER NOT NULL DEFAULT 0,
  harness       TEXT,
  run_id        TEXT,
  joined_at     TEXT NOT NULL,
  last_seen_at  TEXT NOT NULL
) STRICT;

CREATE INDEX participants_map ON participants (map_id, kind);
CREATE UNIQUE INDEX participants_run ON participants (run_id) WHERE run_id IS NOT NULL;

CREATE TABLE runs (
  id            TEXT PRIMARY KEY,
  map_id        TEXT NOT NULL REFERENCES maps (id) ON DELETE CASCADE,
  node_id       TEXT NOT NULL,
  task_id       TEXT,                                  -- set before any tool call can arrive
  session_id    TEXT,
  workspace_id  TEXT NOT NULL,
  profile_id    TEXT NOT NULL,
  profile_name  TEXT,
  harness       TEXT NOT NULL,
  template      TEXT NOT NULL,
  lens          TEXT,
  mode          TEXT NOT NULL CHECK (mode IN ('apply','propose')),
  goal          TEXT,
  budget_json   TEXT NOT NULL,                         -- contract.Budget, fully resolved
  usage_nodes   INTEGER NOT NULL DEFAULT 0,
  usage_subagents INTEGER NOT NULL DEFAULT 0,
  usage_depth   INTEGER NOT NULL DEFAULT 0,
  parent_run_id TEXT REFERENCES runs (id),
  root_run_id   TEXT,                                  -- budget pool; = id for top-level runs
  swarm_id      TEXT,
  status        TEXT NOT NULL CHECK (status IN ('queued','running','waiting','review','done','failed','stopped')),
  waiting_reason TEXT,
  transport     TEXT NOT NULL CHECK (transport IN ('mcp','bridge','prompt')),
  participant_id TEXT NOT NULL,                        -- the agent participant
  started_by    TEXT NOT NULL,                         -- the human participant
  proposal_id   TEXT,
  summary       TEXT,
  error         TEXT,
  last_output   TEXT,
  reported      INTEGER NOT NULL DEFAULT 0,
  report_status TEXT,
  stop_requested INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  ended_at      TEXT
) STRICT;

-- task_id -> run: the access check for every Kandev MCP tool call.
CREATE UNIQUE INDEX runs_task ON runs (task_id) WHERE task_id IS NOT NULL;
CREATE INDEX runs_session ON runs (session_id) WHERE session_id IS NOT NULL;
CREATE INDEX runs_map_status ON runs (map_id, status);
CREATE INDEX runs_parent ON runs (parent_run_id) WHERE parent_run_id IS NOT NULL;
CREATE INDEX runs_root ON runs (root_run_id);
CREATE INDEX runs_swarm ON runs (swarm_id) WHERE swarm_id IS NOT NULL;
CREATE INDEX runs_started_by ON runs (started_by, created_at);  -- audit list, guest rate limits

-- Agent-chosen @refs, scoped to the root run so sub-agents and later patch
-- blocks can resolve each other's refs.
CREATE TABLE run_refs (
  root_run_id   TEXT NOT NULL,
  ref           TEXT NOT NULL,
  node_id       TEXT NOT NULL,
  created_by_run_id TEXT NOT NULL,
  PRIMARY KEY (root_run_id, ref)
) STRICT, WITHOUT ROWID;

-- Processed mindmap-patch blocks, so a re-delivered message.added is a no-op.
CREATE TABLE patch_blocks (
  run_id        TEXT NOT NULL,
  message_id    TEXT NOT NULL,
  block_index   INTEGER NOT NULL,
  ok            INTEGER NOT NULL,
  error_json    TEXT,
  processed_at  TEXT NOT NULL,
  PRIMARY KEY (run_id, message_id, block_index)
) STRICT, WITHOUT ROWID;

CREATE TABLE proposals (
  id            TEXT PRIMARY KEY,
  map_id        TEXT NOT NULL REFERENCES maps (id) ON DELETE CASCADE,
  run_id        TEXT NOT NULL REFERENCES runs (id),
  base_rev      INTEGER NOT NULL,
  status        TEXT NOT NULL CHECK (status IN ('open','ready','accepted','rejected','partial')),
  summary       TEXT,
  author_participant_id TEXT NOT NULL,
  resolved_by   TEXT,
  created_at    TEXT NOT NULL,
  resolved_at   TEXT
) STRICT;

CREATE INDEX proposals_map_status ON proposals (map_id, status);
CREATE UNIQUE INDEX proposals_run ON proposals (run_id);

-- One row per proposed op, in the order the run wrote them.
CREATE TABLE proposal_items (
  proposal_id   TEXT NOT NULL REFERENCES proposals (id) ON DELETE CASCADE,
  seq           INTEGER NOT NULL,
  op_id         TEXT NOT NULL,
  op_json       TEXT NOT NULL,                         -- full contract.Op (rev 0)
  status        TEXT NOT NULL CHECK (status IN ('pending','accepted','rejected','conflict')),
  node_ids_json TEXT NOT NULL,                         -- []string, for per-node review
  error_json    TEXT,                                  -- contract.Error when status = conflict
  applied_rev   INTEGER,                               -- rev it committed at, when accepted
  PRIMARY KEY (proposal_id, seq)
) STRICT, WITHOUT ROWID;

CREATE UNIQUE INDEX proposal_items_op ON proposal_items (op_id);

-- Attachment metadata. Bytes live in assets/<sha256[0:2]>/<sha256>.
CREATE TABLE assets (
  id            TEXT PRIMARY KEY,
  map_id        TEXT NOT NULL REFERENCES maps (id) ON DELETE CASCADE,
  node_id       TEXT NOT NULL,
  kind          TEXT NOT NULL CHECK (kind IN ('image','file')),
  mime          TEXT NOT NULL,
  bytes         INTEGER NOT NULL CHECK (bytes > 0),
  sha256        TEXT NOT NULL,
  filename      TEXT,
  caption       TEXT,
  source_url    TEXT,
  width         INTEGER,
  height        INTEGER,
  author_participant_id TEXT NOT NULL,
  author_run_id TEXT,
  detached      INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL
) STRICT;

CREATE INDEX assets_node ON assets (node_id) WHERE detached = 0;
CREATE INDEX assets_sha ON assets (sha256);              -- garbage collection of unreferenced files
CREATE INDEX assets_map ON assets (map_id);

-- Sharing state per tunnel session. The share token itself is in tokens.
CREATE TABLE share_links (
  id            TEXT PRIMARY KEY,
  map_id        TEXT NOT NULL REFERENCES maps (id) ON DELETE CASCADE,
  provider      TEXT NOT NULL CHECK (provider IN ('cloudflared','tailscale','ngrok')),
  status        TEXT NOT NULL CHECK (status IN ('starting','active','stopped','error')),
  public_host   TEXT,                                  -- e.g. random-words.trycloudflare.com
  token_id      TEXT,                                  -- tokens.id of the mms1 token
  error         TEXT,
  created_by    TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  stopped_at    TEXT
) STRICT;

-- At most one live share per map.
CREATE UNIQUE INDEX share_links_live ON share_links (map_id) WHERE status IN ('starting','active');

-- Every bearer token, hashed. kind: mml local, mms share, mmg guest, mmr run, mmp personal.
CREATE TABLE tokens (
  id            TEXT PRIMARY KEY,
  kind          TEXT NOT NULL CHECK (kind IN ('mml','mms','mmg','mmr','mmp')),
  hash          TEXT NOT NULL,                         -- sha256(raw token), hex
  map_id        TEXT REFERENCES maps (id) ON DELETE CASCADE,  -- NULL = all maps (mml without map, mmp)
  run_id        TEXT REFERENCES runs (id) ON DELETE CASCADE,  -- mmr only
  participant_id TEXT,                                 -- mml host, mmg guest, mmr agent
  share_link_id TEXT REFERENCES share_links (id),      -- mms, and mmg (dies with its share)
  label         TEXT,                                  -- mmp: user-given name
  created_at    TEXT NOT NULL,
  expires_at    TEXT,                                  -- NULL = until revoked
  last_used_at  TEXT,
  revoked_at    TEXT
) STRICT;

CREATE UNIQUE INDEX tokens_hash ON tokens (hash);
CREATE INDEX tokens_run ON tokens (run_id) WHERE run_id IS NOT NULL;
CREATE INDEX tokens_share ON tokens (share_link_id) WHERE share_link_id IS NOT NULL;
CREATE INDEX tokens_map_kind ON tokens (map_id, kind) WHERE revoked_at IS NULL;

-- Drawer activity feed (also the audit list of who started what).
CREATE TABLE activity (
  id            TEXT PRIMARY KEY,
  map_id        TEXT NOT NULL REFERENCES maps (id) ON DELETE CASCADE,
  type          TEXT NOT NULL,
  actor_participant_id TEXT NOT NULL,
  actor_run_id  TEXT,
  summary       TEXT NOT NULL,
  node_ids_json TEXT NOT NULL DEFAULT '[]',
  count         INTEGER,
  rev           INTEGER,
  ts            TEXT NOT NULL
) STRICT;

CREATE INDEX activity_map_ts ON activity (map_id, ts DESC);

-- Plugin-wide key/value settings: loopback port, export dir default, kill-switch state.
CREATE TABLE kv (
  key           TEXT PRIMARY KEY,
  value         TEXT NOT NULL
) STRICT, WITHOUT ROWID;
