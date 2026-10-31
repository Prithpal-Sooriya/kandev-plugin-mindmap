# Mind Map plugin protocol (contract v1)

This is the wire contract between the three parts of the plugin:

- **Backend**: the Go plugin process (`server/`).
- **Canvas UI**: the in-Kandev bundle and the standalone guest web app (`ui/`).
- **Agent bridge**: the Pi extension and the `kandev-mindmap-mcp` stdio server (`bridge/`).

Types live in `server/contract` (Go) and `ui/src/contract` (TS). Both are checked against the shared JSON fixtures in
`contract/fixtures`. Agent tool schemas live in `contract/tools/*.json`, and `manifest.yaml` is generated from them
(`npm run sync:tools`). The data model itself is not repeated here: see the Go doc comments.

All examples are synthetic.

Contents:

1. [Transports at a glance](#1-transports-at-a-glance)
2. [Tokens and access](#2-tokens-and-access)
3. [Kandev webhooks](#3-kandev-webhooks)
4. [Loopback HTTP routes](#4-loopback-http-routes)
5. [WebSocket protocol](#5-websocket-protocol)
6. [Long-poll fallback](#6-long-poll-fallback)
7. [Concurrency model](#7-concurrency-model)
8. [Agent tools](#8-agent-tools)
9. [Run lifecycle and status](#9-run-lifecycle-and-status)
10. [Prompt fallback: `mindmap-patch` blocks](#10-prompt-fallback-mindmap-patch-blocks)
11. [Error codes](#11-error-codes)
12. [Storage](#12-storage)
13. [Phase 1a implementation notes](#13-phase-1a-implementation-notes)

---

## 1. Transports at a glance

```
 in-Kandev UI ──(1) host.api.fetch ──> Kandev ──gRPC──> plugin  webhooks: session, asset, sync, submit
      │
      └──(2) ws://127.0.0.1:<port>/m/<mml token>/ws ───────────> plugin loopback server
 guest browser ── https://<tunnel>/m/<mms token>/ ──tunnel──────> plugin loopback server
 Claude/Cursor ── Kandev MCP kandev_kandev_plugin_mindmap_<tool> ─> plugin InvokeAgentTool
 Pi / other MCP ── bridge ── POST http://127.0.0.1:<port>/agent/v1/tools/<tool> ─> plugin
 any harness ── chat reply with ```mindmap-patch``` ── message.added event ─> plugin
```

- The plugin listens on `127.0.0.1:<port>`. The port is picked at random on first start and saved in plugin state so
  it survives restarts (Kandev restarts the plugin on config save). If it is taken, the plugin picks a new one and
  saves that. Clients always learn the port from the `session` webhook, and the bridge from its token.
- The server binds loopback only. Remote access happens only through the share tunnel, which forwards to the same
  port; the tunnel can reach only `/m/<share token>/...`. Every other path returns 404 when the request comes in
  through the tunnel. Tunnel requests are detected by the `Cf-Connecting-IP`, `Tailscale-*` or `X-Forwarded-For`
  headers, or by a non-loopback `Host`.
- The desktop shell CSP allows `connect-src http://127.0.0.1:* ws://127.0.0.1:*`, but `img-src` is `'self' data:`.
  So the in-Kandev UI loads **images through the `asset` webhook**, never from the loopback port. Guests load them
  from `/m/<token>/assets/<id>`.
- The loopback server sets `Access-Control-Allow-Origin` to the Kandev origin seen by the `session` webhook, and
  checks `Origin` on WS upgrade (Kandev origin, or the tunnel origin for share tokens).

## 2. Tokens and access

Raw tokens are `<kind>1.<secret>`, or `<kind>1.<port>.<secret>` for bridge tokens. `secret` is 32 random bytes in
base64url without padding (43 chars). Only `sha256(token)` is stored, in the `tokens` table, and lookups compare
hashes in constant time. See `contract.ParseToken` and `contract.FormatToken`.

| Kind | Prefix | Issued by | Scope | Lifetime |
|---|---|---|---|---|
| local | `mml1.` | `session` webhook (Kandev-authenticated) | one map, or all maps if requested without `map_id`; host participant | 12 h, refreshed by calling `session` again |
| share | `mms1.` | Share button | one map; anyone with the link | until Rotate link or Stop sharing |
| guest | `mmg1.` | first `hello` over a share token | one map; one guest participant | 30 days idle; dies with its share token |
| run | `mmr1.<port>.` | run creation | the run's map; the run's agent participant | until the run is terminal + 10 min grace, or revoked |
| personal | `mmp1.<port>.` | plugin settings ("create bridge token") | all maps, as the host; for the bridge outside Kandev | until revoked |

Rules:

- **Guest routes 404 on any token problem.** A missing, wrong, rotated or stopped share token returns `404` with an
  empty body, so a wrong token looks the same as a missing map. Local and bridge routes return `401 unauthorized`.
- **Share scope:** a share token reaches only its map, the asset bytes of that map, the agent-profile list, and the
  run/swarm/compile endpoints for that map. It never reaches other maps, Kandev's UI or API, or tasks outside the map.
  Every agent action goes through the backend under the host's Kandev identity.
- **Guest identity:** `POST /m/<share token>/api/join` with `{name, color}` creates a `human` participant and sets
  the `mm_guest` cookie (`HttpOnly; SameSite=Lax; Path=/m/<share token>/`; `Secure` when the request came through
  the tunnel or TLS). Later requests and `hello` frames resume that participant from the cookie. A `participant_id`
  on `hello` is not a session: those ids are broadcast with every op, so trusting one would let a guest impersonate
  another. A WebSocket with no cookie mints a new guest. Rotating or stopping the share token invalidates every
  guest token for that link. The cookie's idle lifetime is 30 days.
- **Rate limits:** per guest or local session, 20 API calls/s (burst 60), 50 ops/s inside submit (burst 200),
  10 run starts per minute, and 20 asset uploads per minute. Over the limit you get `rate_limited` with `retry_ms`.
  The internal `api` webhook principal is not limited. A bad token still 404s; it is not turned into a 429.
- **Kill switch** (host only, `POST /m/<t>/api/kill`): it revokes every share and guest token, stops the tunnel,
  and asks every active run to stop (see §9). Stopping runs goes through `app.Service.StopAllRuns`, which calls
  the `app.StopAllRuns` hook Phase 2a implements (`Service.Stops`) and then marks non-terminal runs `stopped`.
- **Share secrets.** The `tokens` table stores only a sha256. So the host can show the same URL again after a
  quick tunnel gets a new hostname, the raw share secret is also kept in `share-secrets.json` (mode `0600`) in the
  plugin data directory. That file is not served. Guest, local, run and personal tokens are not written there.

### Run token lifecycle

1. **Issue.** When the backend creates a run, it mints `mmr1.<port>.<secret>` and stores its hash with `run_id`
   and `map_id`.
2. **Deliver.** The launch prompt includes the token in a fenced block, together with the instruction to load the
   tool schemas first. The bridge reads it from `MINDMAP_TOKEN`, from a per-call `run_token` argument (bridge-only,
   see §8.3), or from its config file.
3. **Use.** `Authorization: Bearer mmr1....` on `/agent/v1/*`. The token's run decides the map, the actor, the mode
   (apply or propose) and the budget.
4. **Grace.** When the run reaches a terminal status (`done`, `failed`, `stopped`), the token keeps working for 10
   minutes, so a late `report` or `write_doc` still lands. After that every call returns `run_ended`. A follow-up
   message that moves the run back to `running` re-opens the token.
5. **Revoke.** Stop, the kill switch, deleting the map, or deleting the run revokes it at once.

### Kandev MCP access check (`task_id → run`)

Kandev agent tools are instance-wide. Every task in every workspace sees them, and the plugin receives `task_id`,
`session_id`, `workspace_id` and `surface`, but no task metadata. So:

1. The backend writes `runs.task_id` as soon as `Host.Tasks().Create` returns, before the agent can call a tool.
2. On every `InvokeAgentTool`, look up the run by `task_id` (unique index).
   - **No run:** return `not_a_run` ("This task is not a mind map run. Start it from a node in the Mind Map plugin.").
     This applies to every tool, including read-only ones. Arbitrary tasks never see map content.
   - **Run found:** act as that run, under the same rules as a run token (map scope, grace, mode, budget).
3. Read-only tools (`context`, `read`, `search`) may read **other maps in the same workspace** (`read.map_id`,
   `search.scope=workspace`). Write tools touch only the run's own map.
4. Sub-agents spawned by `spawn` are separate tasks with their own run rows (`parent_run_id`, `root_run_id`), so the
   same rule covers them.

## 3. Kandev webhooks

All webhooks are `access: authenticated`. They are the only routes the in-Kandev UI needs when loopback is blocked.
They are called through `host.api.fetch("/api/plugins/kandev-plugin-mindmap/webhooks/<key>")`.

| Key | Method | Body / query | Response |
|---|---|---|---|
| `session` | POST | `{map_id?}` | `SessionResponse`: `base_url`, local `token`, `participant`, `expires_at` |
| `asset` | GET | `?map_id=&id=` | asset bytes with the stored `Content-Type` and `Cache-Control: private, max-age=31536000, immutable` (assets are content-addressed) |
| `sync` | GET | `?map_id=&cursor=&wait=` | `SyncResponse` (see §6) |
| `submit` | POST | `SubmitRequest` + `?map_id=` | `SyncResponse` holding the ack/reject/rebase for this batch |
| `api` | POST | `{method, path, body}` | proxies one `/api/*` call from §4 for the fallback path |

The Phase 0 spike webhooks (`debug-launch`, `probe`, `logs`, `hold`, ...) were removed in Phase 1a. The
`api` webhook gives the fallback path the full `/api` surface without one webhook per route.

## 4. Loopback HTTP routes

`<t>` is a local (`mml1.`) or share (`mms1.`) token. Guests also send the `mm_guest` cookie. JSON bodies use the
contract types. Errors return `{"error": Error}` with the HTTP status from `ErrorCode.HTTPStatus()`.

| Route | Purpose |
|---|---|
| `GET /m/<t>/` | standalone web app (same canvas code, built with its own React) |
| `GET /m/<t>/ws` | WebSocket (§5) |
| `GET /m/<t>/api/maps` | `MapSummary[]`. Local tokens only; share tokens see their one map |
| `POST /m/<t>/api/maps` | `CreateMapRequest` → `Map` (local tokens only) |
| `GET /m/<t>/api/maps/<map>/snapshot` | `Snapshot` |
| `GET /m/<t>/api/maps/<map>/sync?cursor=&wait=` | long-poll (§6) |
| `POST /m/<t>/api/maps/<map>/ops` | `SubmitRequest` → `SyncResponse` |
| `GET /m/<t>/api/maps/<map>/ops` | `?from=<rev>&to=<rev>` → `{ops: Op[]}` |
| `POST /m/<t>/api/maps/<map>/runs` | `StartRunRequest` → `StartRunsResponse` |
| `POST /m/<t>/api/maps/<map>/swarms` | `SwarmRequest` → `StartRunsResponse` |
| `POST /m/<t>/api/maps/<map>/compile` | `CompileRequest` → `StartRunsResponse` (section runs + synthesis run) |
| `POST /m/<t>/api/maps/<map>/suggest` | `SuggestRequest` → `SuggestResponse` (one-shot `InvokeUtilityAgent`) |
| `POST /m/<t>/api/runs/<run>/stop` | asks the agent to stop (§9). `204` |
| `POST /m/<t>/api/runs/<run>/message` | `MessageRunRequest` → `Messages().Send`. `204` |
| `POST /m/<t>/api/runs/<run>/approve` | host only: release a guest run held for approval |
| `POST /m/<t>/api/maps/<map>/nodes/<node>/answer` | `AnswerQuestionRequest` → `{ops, rev}`: answers an `ask_human` question node and relays the answer to the asking agent (§9) |
| `GET /m/<t>/api/repos?map_id=<map>` | `RepoInfo[]`: repositories of the map's workspace (for `execute`). Share tokens use their map |
| `GET /m/<t>/api/runs/tasks` | `RunTasksResponse`: Kandev task ids that are map runs (all-maps local tokens only) |
| `GET /m/<t>/api/tasks/<task>/run` | `TaskRunInfo`: the run behind a Kandev task, the path to its node and its branch (local tokens only) |
| `POST /m/<t>/api/proposals/<id>/review` | `ReviewProposalRequest` → `Proposal` |
| `POST /m/<t>/api/maps/<map>/assets` | multipart upload (`file`, `node_id`, `caption?`) → `Asset`. Then submit `asset.attach` |
| `GET /m/<t>/assets/<id>` | asset bytes (guests; the in-Kandev UI uses the `asset` webhook) |
| `GET /m/<t>/api/profiles` | `AgentProfile[]` via `AgentProfiles().List` |
| `POST /m/<t>/api/maps/<map>/fork` | `{rev?}` → `Map` (fork at rev, default head) |
| `GET /m/<t>/api/maps/<map>/export.zip` | compiled docs as a zip |
| `POST /m/<t>/api/maps/<map>/share` | host only: start the tunnel if needed, mint a 256-bit share token, return `ShareLink` whose `url` is `https://<host>/m/<mms token>/` |
| `POST /m/<t>/api/maps/<map>/share/rotate` | host only: invalidate the current link and mint another. The tunnel stays up |
| `DELETE /m/<t>/api/maps/<map>/share` | host only: stop sharing this map. `204`. The tunnel stops when no map is shared |
| `GET /m/<t>/api/maps/<map>/share` | host only: current `ShareLink`, or `404` when the map is not shared |
| `POST /m/<t>/api/join` | share token only: `{name, color}` → `Participant`, and sets `mm_guest` |
| `GET /m/<t>/api/maps/<map>/audit` | `ActivityEvent[]` of who started or spawned a run (`run.started`, `run.spawned`) |
| `GET /m/<t>/api/settings/share` | host only: plugin-wide `ShareSettings` |
| `PUT /m/<t>/api/settings/share` | host only: replace `ShareSettings` |
| `POST /m/<t>/api/kill` | host only: kill switch. `204` |

`ShareSettings.provider` is `cloudflared` (default), `tailscale` or `ngrok`. A zero cap means no plugin-level limit.
Per-map `MapSettings` still apply and the tighter of the two wins. `require_guest_run_approval` is off by default.
When it is on, a guest `POST .../runs` returns `403 forbidden` until Phase 2a queues the run with
`waiting_reason=approval`. The host and agent participants are never held. Run launch calls
`Service.CheckRunCaps` before creating a run; a swarm that does not go through `StartRun` must call it too.
`Service.GuestRunNeedsApproval` (and `share.GuestRunNeedsApproval`) is the approval check.

A missing tunnel binary is `not_installed` (HTTP 503). The message includes the install command, for example
`brew install cloudflared`. Host-only routes answer `403` to a valid share token. Token problems stay an empty `404`.
| `GET /agent/v1/tools` | bridge: `ToolListResponse` (run or personal token) |
| `POST /agent/v1/tools/<name>` | bridge: body = tool arguments, response = `ToolResult` (§8.3) |

"Host only" means a local token, or the host participant. Share tokens never get host routes, even though guests
otherwise have the same capabilities as the host.

## 5. WebSocket protocol

One connection per tab per map: `ws://127.0.0.1:<port>/m/<t>/ws?map_id=<map>`, or `wss://<tunnel>/m/<t>/ws` for
guests. Each frame is one JSON object, `{"type": <MsgType>, "payload": {...}}` (`contract.Message`).

### Client → server

| type | payload | notes |
|---|---|---|
| `hello` | `HelloPayload` | must be first. `contract: 1`. `since_rev` asks for catch-up instead of a snapshot. `participant_id` is ignored; the `mm_guest` cookie is the session |
| `submit` | `{ops: Op[]}` | at most 200 ops, applied atomically in order as one batch |
| `presence.update` | `{presence: Presence}` | at most 20/s; the server coalesces and rebroadcasts at most every 50 ms |
| `output.subscribe` | `{run_id}` | start streaming `output` for a run (the inspector is open) |
| `output.unsubscribe` | `{run_id}` | |
| `ping` | `{nonce?}` | every 20 s; the server closes after 60 s of silence |

### Server → client

| type | payload | when |
|---|---|---|
| `welcome` | `WelcomePayload` | reply to `hello`: your participant, server time, long-poll cursor |
| `snapshot` | `Snapshot` | after `welcome`, unless catch-up was possible |
| `ops` | `{ops: Op[], rev}` | committed ops from **others**, in rev order. Also used for catch-up after `hello.since_rev` |
| `ack` | `{acks: [{id, rev}]}` | your submit committed unchanged |
| `reject` | `{ids, error}` | your whole submit batch was refused |
| `rebase` | `{op}` | one of your ops committed **changed**. Replace your optimistic copy with this one. An `ack` for the rest of the batch follows |
| `presence` | `{presence?: Presence[], left?: participant_id[]}` | presence changed |
| `participant` | `{participant}` | joined, renamed or online changed |
| `run` | `{run}` | any run field changed, including `last_output`, which updates at most 1/s per run |
| `proposal` | `{proposal}` | created, item added, or reviewed |
| `activity` | `{events}` | new feed lines |
| `output` | `{run_id, lines}` | agent output for subscribed runs, batched every 100 ms |
| `error` | `{error, fatal?}` | `fatal` is followed by a close. `resync`: send `hello` again without `since_rev` |
| `pong` | `{nonce?}` | |

### Ordering and client algorithm

- The server is the single writer per map. It assigns `rev` = previous + 1 to each committed op, with no gaps, and
  all clients observe ops in rev order.
- The client keeps `confirmed` (server state at `rev`) and `pending` (its own unacked ops, in send order). The view
  is `confirmed` with `pending` applied on top. This is the optimistic layer, so your own keystrokes render in the
  same frame.
- On `ops`: apply the ops to `confirmed`, then re-apply `pending` on top. Field-level last-writer-wins (§7) makes
  this deterministic without transforms.
- On `ack`: move those ops from `pending` into `confirmed` at the acked rev. Ops from others with lower revs have
  already arrived, because the server sends the `ops` broadcast before the `ack` on each connection.
- On `rebase`: same as `ack`, but use the server's op.
- On `reject`: drop those ops from `pending` and show the error (for example `cycle`: "Can't move a node into its
  own branch").
- **Reconnect:** send `hello` with `since_rev` = your confirmed rev and `participant_id`, then re-submit `pending`.
  Op ids make re-submission idempotent: an op id the server already committed is acked again with its original rev,
  not applied twice. The server keeps op ids for 24 h for this purpose.
- The server keeps the full op log, so catch-up works from any rev unless the map was compacted. If it can't catch
  up, it replies with a `snapshot` instead of `ops`.

## 6. Long-poll fallback

Use it when the WebSocket cannot open (a blocked loopback, or a proxy that drops upgrades). It uses the same
messages through `GET .../sync` (webhook `sync` in Kandev, or `/m/<t>/api/maps/<map>/sync` on loopback) and
`POST .../submit` or `.../ops`.

- `GET sync?cursor=<c>&wait=<s>` returns `SyncResponse{cursor, messages: Message[]}`. The messages are exactly the
  server → client frames the WS would have sent since `cursor`, in order (`ops`, `run`, `presence`, `activity`,
  `output`, ...).
- With no cursor, the response is `[welcome, snapshot]`.
- **Hold:** if nothing is pending, the server holds the request until something happens or `wait` seconds pass,
  whichever comes first. `wait` is clamped to **at most 20 s** (`contract.LongPollMaxHold`). Kandev kills webhook
  requests at 30 s. An empty `messages` with a new cursor is normal; poll again at once.
- The cursor is opaque (`c<rev>.<seq>`). The server keeps a per-map ring buffer of the last 2,000 non-op messages
  plus the op log. A cursor older than the buffer gets `[snapshot]` and a fresh cursor.
- Presence goes in `SubmitRequest.presence` (with `ops` empty if there is nothing else to send). Presence over
  long-poll is best-effort and may lag one poll.
- `output` is delivered only for runs subscribed through `?subscribe=<run_id>,...` on `sync`.
- Clients switch back to the WS when it becomes reachable. Both transports share the cursor and rev, so switching
  needs no snapshot.

## 7. Concurrency model

- **Server-authoritative, monotonic rev.** Each map has one writer goroutine and one op log. A `submit` is one
  SQLite transaction: validate each op, apply it, append it with the next rev, then commit. If any op fails, the
  transaction rolls back and the server sends `reject` for every op in the batch.
- **Validation order per op:** `Op.Validate()` (structural), then state checks: the node exists and is not
  tombstoned, tree checks via `ParentIndex.CheckMove/CheckMerge/CheckDelete/CheckCreate`, soft lock, claim, budget.
- **Field-level last-writer-wins.** Every node field in `NodeFields` is its own register. A `node.update` that sets
  `title` never conflicts with one that sets `body_md`. Two writes to the same field: the higher rev wins, with no
  merge. `append_body`, `add_tags`, `remove_tags` and `add_sources` are merge-style and commute, so concurrent
  appends both survive. `set.tags` replaces the whole set.
- **Moves are validated on the server against the current tree,** not the client's base. A move that would create
  a cycle at commit time is rejected with `cycle`, even if it was legal on the client's stale view. The client rolls
  it back.
- **Order keys** are fractional (`contract.OrderBetween`). On a collision (two inserts at the same spot computing
  the same key), the server keeps the first and rewrites the second to a key between the winner and its next
  sibling, then sends `rebase` to the second author.
- **Deleted targets:** an update to a tombstoned node is rejected with `deleted`. A create or move under a
  tombstoned parent is rejected with `deleted`.
- **Soft locks.** While a participant edits a title or body, its presence carries `editing {node_id, field,
  expires_at}`, renewed every 5 s with a 15 s TTL. The server rejects **other** participants' `set` of that field
  with `locked`, and agents get the same error from `update`. Merge-style fields are never locked: an agent can
  still `append_body` while you type. Locks are advisory and vanish when they expire, so a crashed tab cannot hold
  a node.
- **Claims** are persistent ops (`node.claim`/`node.release`) with an expiry. A claim by a different participant
  that has not expired makes `claim` fail with `claimed`. Humans are never blocked by claims; they are a
  coordination hint for agents.
- **Proposals.** In propose mode, write tools append ops to the run's proposal, not the log. The ops are fully
  validated against the live map at proposal time, and their node ids are minted then, so ghosts can reference
  each other. Reviewing applies the accepted items as one batch, in their original order, each with `proposal_id`
  set. An item that no longer applies (its target was deleted or moved into a cycle) becomes `conflict` with an
  `error`, and the rest still apply. Items that depend on a rejected or conflicting `node.create` become `conflict`
  too.
- **Undo** is per author: the server stores an inverse with every op (`ops.inverse_json`). Undo submits the
  inverse as a new op. It is never a rewind, so others' later edits survive.
- **Time travel:** replay uses the log from rev 1. Fork copies the state at a rev into a new map with
  `forked_from`.

## 8. Agent tools

Thirteen tools, declared in `contract/tools/<name>.json` as `{name, description, annotations, input_schema}`:
`context`, `read`, `search`, `add`, `update`, `attach`, `restructure`, `link`, `claim`, `spawn`, `ask_human`,
`write_doc`, `report`.

- **Names:** Kandev exposes the tools as `kandev_kandev_plugin_mindmap_<name>`. The bridge exposes them as
  `mindmap_<name>`. Prompts refer to them by suffix ("call the mind map `context` tool").
- **Schemas** are portable JSON Schema draft 7: the root is an object, there is no root `oneOf`/`anyOf`/`allOf`,
  there is no `$ref`, and every object sets `additionalProperties: false`. The `add` nesting is unrolled three
  levels, not recursive, because several model providers reject recursive schemas. Cross-field rules (for example
  "exactly one of url, file_path or base64") are enforced by `contract.DecodeToolArgs`, which every transport runs.
  Total compact schema size is about 16 KiB (the Kandev limit is 64 KiB per tool).
- **Node arguments** take a node id (`nd_...`) or `@ref`. A ref is set by `add`, `restructure` (regroup) or
  `write_doc`, and is scoped to the run (table `run_refs`).
- **Defaults:** `parent_id` and `node_id` default to the run's node where noted. `kind` defaults to `idea`, and the
  mode comes from the run.

### 8.1 Results

Every call returns `ToolResult{text, structured?, is_error?}`. `text` is what the model reads; it is never empty and
is written as compact, id-tagged lines.

- **Errors:** `is_error: true`, text `error <code>: <field>: <message>`, structured `{"error": Error}`. Messages say
  how to fix the call (for example `error bad_request: nodes: required: at least one node`).
- **Write tools** return structured `WriteOutcome{applied, proposal_id?, rev, node_ids, refs, budget}`. `applied`
  is false in propose mode.
- `read`/`context` outline lines look like:
  `- [nd_pricing000001] idea · running · 2 src · conf 0.6 · Pricing model`, indented two spaces per level.
  `read` returns structured `{rev, next_cursor?}`.
- `spawn` returns `{runs: [{run_id, node_id, task_id, status}]}`. `ask_human` returns `{node_id}`. `write_doc`
  returns `{node_id, path}`.
- Results stay under 1 MiB. `read` pages at `limit` and summarises bodies beyond 2,000 chars.

### 8.2 Per-tool rules the schemas cannot express

| Tool | Rule |
|---|---|
| `add` | at most 100 nodes per call across all levels, 3 levels deep. `kind: doc` is not allowed (use `write_doc`). Each node counts against `max_nodes`; depth below the run node counts against `max_depth`. If the budget is exceeded, nothing is written (`budget_exceeded`) |
| `update` | at least one change. `body` and `append_body` are mutually exclusive. `set` on a soft-locked field returns `locked` |
| `attach` | exactly one of `url`, `file_path`, `base64`. `file_path` must be absolute and readable by the plugin process. The bridge reads relative paths itself and sends `base64`. `base64` needs `mime_type`. Max 10 MB decoded. MIME must be in `AllowedAssetMIME` |
| `restructure` | per-op required fields (see the description). All ops are one batch: all or nothing |
| `claim` | fails with `claimed` if another participant's claim has not expired. A claim covers the subtree for halos and swarm coordination |
| `spawn` | templates `research`, `explore`, `swarm`, `critique`, `expand` or `examples` (default `research`). Each agent costs one `max_subagents` from the root run's pool. Child budgets are clamped to what is left |
| `ask_human` | creates a `question` node and moves the run to `waiting(question)`. The answer (`question.answer` op) is sent back to the agent with `Messages().Send` |
| `write_doc` | `path` is relative and ends in `.md`. The doc node is placed under `parent_doc_id`, or the run node |
| `report` | sets `run.summary` and `run.reported`, and drives the status (§9). In propose mode it moves the proposal from `open` to `ready` |

### 8.3 Bridge HTTP API

- `GET /agent/v1/tools` returns `ToolListResponse`. The bridge registers these declarations at startup.
- `POST /agent/v1/tools/<name>` with headers `Authorization: Bearer <mmr1|mmp1 token>`,
  `X-Mindmap-Contract: 1`, `Content-Type: application/json`. The body is the tool arguments. The response is
  `200` + `ToolResult` even for tool errors (the model must see them). Non-200 means a transport problem:
  `401` (bad token), `426` (contract version), `413` (body over 16 MiB) or `5xx`.
- Calls time out after 25 s server-side (Kandev's 30 s budget minus margin, kept the same on every transport).
- **Token discovery in the bridge,** in this order: the `MINDMAP_TOKEN` env var, then the bridge config file
  (`~/.config/kandev-mindmap/config.json`, `{token}`), then a per-call `run_token` string argument. The bridge adds
  `run_token` to every tool's schema **only** when it has no token, and strips it before calling the API. The token
  carries the port, so no URL is needed. `MINDMAP_URL` overrides the base URL for testing.
- With a personal token the bridge has no run. Write tools then act as the host in `apply` mode with no budget, and
  `context` returns the map list instead of a run brief. Each call needs `node_id` or `parent_id`; there is no run
  node default.

## 9. Run lifecycle and status

`Run.status ∈ queued | running | waiting | review | done | failed | stopped` (with `waiting_reason ∈ question |
turn_ended | approval`).

1. **Start.** `POST runs` (or `spawn`, or a swarm or compile) creates a `runs` row (`queued`), an agent
   participant, and a run token. The backend then calls `Host.Tasks().Create` with `StartAgent: true`,
   `Launch{AgentProfileID, Prompt}` and `Metadata{mindmap_id, node_id, run_id, parent_run_id?}` (plus
   `Repositories` when a repo is given), and stores `task_id` before returning. If `Create` fails, the run is
   returned `failed` with `error` set (HTTP 200), its token revoked and its claims released. If the host requires
   approval for guest runs, the run stays `waiting(approval)` with no task until approved.
2. **Status from events.** `task_session.state_changed` is the source of truth. Spike finding: `agent.completed`
   and `agent.failed` may never fire. The event's `task_id` maps to the run, and `new_state` is read defensively;
   when it is missing, the event is ignored. `contract.NextRunStatus` is the pure rule:

   | `new_state` | Run status |
   |---|---|
   | `CREATED`, `STARTING` | `queued` (never downgrades a started run) |
   | `RUNNING` | `running` (also re-opens `waiting`/`review`/`done` after a follow-up message) |
   | `WAITING_FOR_INPUT`, `IDLE`, `COMPLETED` | stop requested → `stopped`; open question → `waiting(question)`; reported → `review` if report said review or a proposal is open, else `done`; proposal open → `review`; `COMPLETED` → `done`; else `waiting(turn_ended)` |
   | `FAILED` | `failed` |
   | `CANCELLED` | `stopped` |

   `agent.completed`/`agent.failed`, if they arrive, are treated as `COMPLETED`/`FAILED`. The node's ring follows
   `contract.NodeStatusForRun`.
3. **Output.** `message.added` (and `agent.stream.*` when available) for the run's session becomes `output`
   lines, and updates `run.last_output`. The same event feeds the `mindmap-patch` parser (§10).
4. **Stop.** The Host API cannot stop a session (spike finding 5). Stop marks the run `stopped` right away (with
   `stop_requested` and `ended_at`), revokes its run tokens, releases its claims, and sends
   `Messages().Send("Stop now: … call report with status review and end your turn.")`. Later session states keep
   it `stopped`. Bridge calls fail at once (token revoked); Kandev MCP calls from the task keep working for the
   10-minute grace (so the agent can `report`), then fail with `run_ended`. Stopping a
   stopped run is a no-op. The UI also links to the task. A `message` to a stopped run re-opens it (clears
   `stop_requested`; bridge runs get a fresh token in the message) and the next `RUNNING` state makes it `running`.
5. **Budget.** `usage` is summed across the run tree (`root_run_id`). Writes that would exceed it fail with
   `budget_exceeded`; so does a `spawn` beyond `max_subagents`. `max_minutes` is enforced by a 30 s watchdog that
   stops the run (as in 4) with `error: "time budget of N minutes used up"`.
6. **Swarms.** `POST swarms` creates a **coordinator** run (`template: swarm`, no task, no lens) that owns the shared
   budget pool (`max_nodes` = per-agent budget × agents) and the `swarm_id`. For each member it adds a
   `"<Lens> lens"` idea child under the target node and starts a lens run there (`parent_run_id` = coordinator,
   `lens` set) that claims its branch. The coordinator's status is derived from its children: any
   `running`/`queued` → `running`; else any `waiting` → `waiting`; else any `review` → `review`; else all
   `stopped` → `stopped`; all `failed` → `failed`; else `done`. Stop and message on the coordinator fan out to the
   children. `StartRunsResponse.runs` is `[coordinator, ...lens runs]`.
7. **Questions.** `ask_human` adds a question node and puts the run in `waiting(question)`. Answering (WS
   `question.answer` op or `POST .../nodes/<node>/answer`) writes the answer, sends it to the agent with
   `Messages().Send`, and, when no other question of the run is open, moves the run back to `running`.

## 10. Prompt fallback: `mindmap-patch` blocks

Use this for harnesses with neither Kandev MCP tools nor the bridge (`Run.transport = prompt`). The launch prompt
holds the outline (the same text `context` would return) and asks the agent to reply with fenced blocks:

- The fence info string is exactly `mindmap-patch`. The body is one JSON object: `{"v": 1, "calls": [{"tool":
  <name>, "args": <tool arguments>}]}`.
- `tool` is any **write** tool (`add`, `update`, `attach` with `url` only, `restructure`, `link`, `claim`, `spawn`,
  `ask_human`, `write_doc`, `report`). Read-only tools are rejected: the outline is already in the prompt.
- `args` are exactly the tool's arguments, validated by the same `DecodeToolArgs`. At most 50 calls per block.
- Calls run in order through the shared tool implementation, as the run. Refs from earlier calls, blocks and
  messages in the same run resolve.
- The backend parses every `message.added` from the run's agent with `contract.ExtractPatches`. An unterminated
  final fence is ignored, since the agent may still be streaming; the block is parsed when the completed message
  arrives. Each block is processed once, keyed by `(message_id, block index)`.
- **Feedback:** if any call fails, the backend sends one follow-up message listing the failed calls and errors, and
  the ref → id map. Successful blocks get no reply, so the conversation does not loop.

Example reply:

```mindmap-patch
{"v": 1, "calls": [
  {"tool": "add", "args": {"parent_id": "nd_pricing000001", "nodes": [
    {"ref": "tiers", "title": "Three-tier freemium", "kind": "idea", "confidence": 0.6, "children": [
      {"title": "Example Co: free up to 3 notebooks", "kind": "example",
       "sources": [{"url": "https://example.com/pricing", "quote": "Free for up to 3 notebooks."}]}
    ]}
  ]}},
  {"tool": "link", "args": {"from_id": "@tiers", "to_id": "nd_question0001", "label": "depends on",
    "reason": "Tier limits depend on the target segment."}},
  {"tool": "report", "args": {"summary": "Added a freemium branch with one cited example.", "status": "review"}}
]}
```

## 11. Error codes

`contract.Error{code, message, field?, retry_ms?}` is the same object on every surface.

| Code | HTTP | Meaning |
|---|---|---|
| `bad_request` | 400 | malformed JSON or tool arguments; `field` names the argument |
| `invalid_op` | 400 | an op failed structural validation |
| `cycle` | 400 | the move or merge would make a node its own ancestor |
| `root_immutable` | 400 | the root cannot be moved, deleted or merged |
| `deleted` | 400 | the target node or parent is tombstoned |
| `unauthorized` | 401 | missing, expired or revoked local or bridge token (guest routes use 404) |
| `forbidden` | 403 | the token is valid but its scope doesn't cover the target (for example, writing to another map) |
| `not_a_run` | 403 | agent tool called from a task that is not a mind map run |
| `run_ended` | 403 | the run is terminal and past its grace period |
| `budget_exceeded` | 403 | the run tree's budget is spent |
| `not_found` | 404 | the map, node, run or asset doesn't exist; also any guest token problem |
| `conflict` | 409 | the op no longer applies (duplicate id, stale proposal item) |
| `claimed` | 409 | the node is claimed by another participant |
| `locked` | 409 | the field is soft-locked by another participant |
| `proposal_closed` | 409 | the proposal is already resolved |
| `resync` | 409 | the client is too far behind; send `hello` without `since_rev` |
| `too_large` | 413 | over a size or count limit |
| `unsupported_media` | 415 | the asset MIME type is not allowed |
| `version` | 426 | contract version not supported |
| `rate_limited` | 429 | slow down; see `retry_ms` |
| `not_implemented` | 501 | not built yet (the stub dispatcher returns this for every tool) |
| `fetch_failed` | 502 | the attach URL or file could not be read |
| `not_installed` | 503 | the chosen tunnel binary is not on PATH or in the usual Homebrew locations. `message` includes the install command |
| `unavailable` | 503 | the Host API or tunnel is unavailable; retry |
| `internal` | 500 | bug |

## 12. Storage

`server/store/schema.sql` is migration v1. It uses SQLite through `modernc.org/sqlite` (pure Go) at
`$KANDEV_PLUGIN_DATA_DIR/mindmap.db`, with WAL mode, `foreign_keys=ON` and `busy_timeout=5000`. Asset bytes live in
`$KANDEV_PLUGIN_DATA_DIR/assets/<sha256[0:2]>/<sha256>`, deduplicated by hash. The `assets` table maps ids to hashes.
`PRAGMA user_version` holds the migration number.

## 13. Phase 1a implementation notes

What the backend does today, including additions to and clarifications of the sections above. Clients should code
against these.

**Additions**

| Route | Purpose |
|---|---|
| `POST /m/<t>/api/maps/<map>/undo` | reverts the caller's latest batch that is not undone yet. Response `{ops, rev}` (`OpsPayload`). The inverse ops are also broadcast to **every** WS client, including the caller, as a normal `ops` message |
| `POST /m/<t>/api/tokens/personal` | `{label?}` → `{id, token, label}`: a personal bridge token (`mmp1.<port>.<secret>`). All-maps local tokens only |

- The manifest also subscribes to `message.updated`, so `mindmap-patch` blocks in agent messages that stream in
  through updates are still executed (each block once, keyed by `(message_id, block index)`).
- Routes from §4 that later phases build (compile, suggest, export.zip) answer `501 not_implemented`. Share,
  rotate, stop, status, join, audit, share settings and the kill switch are live (Phase 2d). Runs, swarms, stop,
  message, approve and answers work since Phase 2a (§14). While `require_guest_run_approval` is on, a guest's run
  is created in `waiting(approval)` with no task until the host approves it.

**Clarifications for clients**

- **Duplicate ops.** Catch-up after a reconnect or a restarted plugin can overlap ops you already have. Drop any
  `ops` entry whose `rev` is not greater than your confirmed rev.
- **Long-poll `client_id`.** Pass `client_id=<your hello client id>` on `sync` so your own submitted ops are not
  echoed back (you get them as `ack`/`rebase` from `submit`). The `submit` answer's `cursor` is always `""`: keep
  polling with the cursor from your last `sync`.
- **Cursors survive restarts.** A cursor from before a plugin restart is answered from the op log (one `ops`
  message) when the gap is at most 1,000 ops, otherwise with a `snapshot`.
- **`session` without `map_id`** returns a participant with an empty `id`. The real per-map participant arrives in
  the WS `welcome`.
- **Static guest app.** `GET /m/<t>/` serves `server/web/dist/index.html` (SPA fallback for unknown paths), so the
  build must use relative asset URLs (Vite `base: "./"`). `GET /m/<t>/assets/<name>` serves stored asset bytes when
  `<name>` is an `as_...` id and a file from `dist/assets/` otherwise. All-maps tokens add `?map_id=`.
- **Ghosts.** `Node.pending_proposal_id` is filled in snapshots only. Live changes to proposals arrive as `proposal`
  messages; derive ghosts from their pending items.
- **Propose mode.** `claim`, `ask_human` and the summary that `report` appends to the node are always live, since
  they coordinate rather than change content. Everything else becomes proposal items.
- **Undo limits.** `node.merge`, `question.answer` and `link.delete` cannot be undone (`conflict`). The inverse of
  an update does not remove sources added by `add_sources`, because `NodeFields` cannot set sources. Restoring a
  node also restores tombstoned descendants that were deleted earlier.
- **Guests.** Join with `POST .../api/join` before connecting. The `mm_guest` cookie (HttpOnly, SameSite=Lax,
  Secure on the tunnel) is the session. `hello.participant_id` is not resumed. A tunnel-looking request
  (`Cf-Connecting-Ip`, `X-Forwarded-For`, a `Tailscale-*` header, or a non-loopback `Host`) is allowed only for
  a share token, and only for that token's map. Local tokens, `/agent/v1` and every other path 404. The server
  never proxies those requests to Kandev. Asset uploads are limited to `AllowedAssetMIME` and 10 MiB, and they
  share the per-session upload rate limit.
- **`attach.file_path`.** Kandev does not tell plugins a task's worktree path (`pluginsdk.Task` and `Session` have
  no such field), so the backend accepts absolute paths under `$KANDEV_HOME/tasks` (default `~/.kandev/tasks`,
  where Kandev puts task worktrees) or the temp dirs, after resolving symlinks. Override with
  `MINDMAP_ATTACH_ROOTS`. `attach.url` refuses loopback, private and link-local addresses.

## 14. Phase 2a implementation notes

Agent runs launch real Kandev tasks. Notes for clients and for the sharing work:

- **Templates.** `POST runs` takes `template ∈ research | expand | critique | examples | custom | explore | garden |
  execute` (`swarm` only through `POST swarms`; `compile`/`synthesis` arrive later). `explore` is the autonomous
  explorer: it works within `budget` and may `spawn`. `garden` always runs in `propose` mode and defaults to the
  root node. `execute` needs an `action` node and uses `repo_id` (default: the map's `repo_id`). `custom` needs a
  `goal`. `lens` adds a persona to any template.
- **Profiles.** `profile_id` accepts a profile id or its name. The default is the map's
  `settings.default_profile_id`, then the plugin config `agent_profile`. Without either the request fails with
  `bad_request` on `profile_id`. `GET profiles` returns `{id, name, harness}`; `harness` is detected from the
  agent's display name and profile name (whole words: `claude`, `cursor`, `codex`, `pi`; else `other`).
- **Tool transport.** Claude, Cursor and Codex get `transport: mcp`: their prompt names the Kandev MCP tools
  (`kandev_kandev_plugin_mindmap_<tool>`; Claude: `mcp__kandev__kandev_kandev_plugin_mindmap_<tool>` and an
  instruction to load the deferred tool schemas first). Every other harness gets `transport: bridge`: the prompt
  carries a run token for the `mindmap_*` bridge tools, a map outline, and the `mindmap-patch` fallback (§10).
- **Early events.** Session events for a task id the plugin does not know yet are buffered (2 min) and replayed
  when `Create` returns, so a fast agent's first `RUNNING` is not lost.
- **Task panel.** The in-Kandev bundle registers a task panel (`mindmap-run`) that reads
  `GET tasks/<task>/run` and links to `/mindmap?map=<map>&node=<node>`; the canvas selects `?node=` on load.
