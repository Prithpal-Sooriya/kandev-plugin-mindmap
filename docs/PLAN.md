# Kandev Mind Map plugin: plan

## Context
The Gemini conversation ("AI Mindmapping Tools") found that off-the-shelf FOSS mind maps don't track agent run state, and recommended building a Kandev plugin instead. Kandev is where you run multiple harnesses (Claude, Cursor, pi).

The goal is a very responsive mind map that is also a place to run agents:
- you and agents branch out from a central topic;
- any node can launch real Kandev tasks with a chosen agent;
- running agents research, find examples, capture screenshots, explore on their own and reorganise the map;
- branches compile into documents.

**Decisions you made:**
- A new **standalone** repo, `github.com/Prithpal-Sooriya/kandev-plugin-mindmap` (public, like your other plugins).
- It is built from the official Kandev plugin template and SDK docs, with no code dependency on, or copying from, Brainstorm.
- An agent run started from a node is a real Kandev task, linked back to that node.
- The map is a tree with optional cross-links, laid out automatically.
- Documents are compiled as described below: one readable doc plus a split folder tree.
- Multiplayer: the map runs on your laptop and can be shared through a tunnel with a secret-path link. Guests get the same capabilities as you, including agents.

## What the Kandev SDK gives us (verified in `~/Desktop/projects/kandev-sdk`)
- **Agent tools:** declared in the manifest as `agent_tools` and implemented in `pluginsdk.AgentToolPlugin.InvokeAgentTool`. Kandev's MCP server exposes them as `kandev_<plugin>_<name>`.
  - Limits: 16 tools at most, 30 s per call, 1 MiB results, no streaming.
  - Tools are **instance-wide**, so the plugin restricts access itself using `Context.TaskID` and the task metadata it sets.
  - Specs: `docs/specs/plugins/requirements/agent-tools.md` and `docs/decisions/2026-08-11-plugin-tools-through-kandev-mcp.md`.
- **Launching agents:** `Host.Tasks().Create` with `Launch{AgentProfileID, Prompt}` and `Metadata{mindmap_id,node_id,run_id}`.
  - Follow-ups go through `Messages().Send`.
  - Run status comes from `OnEvent` (`task_session.state_changed`, `agent.completed`/`failed`).
- **Live agent output in the UI:** `host.conversation.useSessionMessages` / `useSessionTurns`, which run over WebSocket.
- **Kandev cannot carry pushes from the plugin to its UI.** Plugin events never reach Kandev's WebSocket or SSE.
  - → The plugin backend runs **its own small HTTP + WebSocket server** on a local port (see Collaboration). Every client connects to it for ops, presence and live agent output pushed to it: the in-Kandev UI, other tabs, and remote guests.
  - If that is blocked, the fallback is a long-poll webhook through Kandev (`GET sync?since=<rev>`).
  - Your own edits apply optimistically, so typing feels instant. Edits from agents and other people should appear within about 100 ms.
- **Storage:** plugin-owned SQLite in `KANDEV_PLUGIN_DATA_DIR`, using pure-Go `modernc.org/sqlite`. Each map gets a monotonic revision plus an op log, which also enables undo, replay and proposals. Image assets go in the same data dir and are served through an authenticated GET webhook.
- **UI bundle:** it must share the host React and has no `react-dom`.
  - → A **custom canvas**: a tidy-tree layout we compute ourselves, HTML nodes, an SVG layer for edges, and CSS-transform pan/zoom. No React Flow. It is small and fast and suits keyboard-first editing.
- **Starting point:** the official `kdlbs/kandev-plugin-template` repo, plus `docs/public/plugins-authoring.md`, `plugins-manifest.md`, `docs/plans/plugins/PLUGIN-API.md` and `.agents/skills/create-kandev-plugin/SKILL.md` in kandev-sdk.

## Data model
- `Map{id, workspace_id, title, repo_id?, settings, rev}`
- `Node{id, map_id, parent_id, order, title, body_md, kind, tags[], confidence?, sources[], assets[], status, run_id?, author, collapsed, color, pending_proposal?}`
- `kind` is one of `topic | idea | research | example | question | decision | risk | action | doc`.
- `sources[]` entries are `{url, title, quote, accessed_at}`. Evidence is part of the data model, not loose text.
- `status` is one of `idle | claimed | running | review | done | failed`.
- `Link{from, to, label, reason, author}`
- `Run{id, node_id, task_id, profile, template, mode: apply|propose, budget, parent_run?}`
- `Op{map_id, rev, type, payload, actor, proposal_id?}`
- `Proposal{id, run_id, ops[], status}`

## Agent tools (13 of the 16 allowed, leaving room to grow)
| Tool | Purpose | Covers |
|---|---|---|
| `mindmap_context` | Returns the run brief, my node, its ancestors and subtree, my budget, and open questions | Every run |
| `mindmap_read` | Reads a map or subtree as an id-tagged outline (depth limit, bodies optional, paged) | Reading sections |
| `mindmap_search` | Searches text, kind, tag and status, optionally across all maps | Finding related ideas and duplicates |
| `mindmap_add` | Adds a nested batch of nodes with kind, body, tags, confidence and **sources** | Research findings, ideas, real-world examples (`kind: example` with a source) |
| `mindmap_update` | Edits fields or appends to a body, adds sources, sets confidence | Refining and adding evidence |
| `mindmap_attach` | Attaches an image or file from a URL, a local path in the agent's worktree, or base64, with a caption and source URL | **Screenshots of web pages** taken with the agent's own browser or Playwright tools, diagrams, PDFs |
| `mindmap_restructure` | Runs batched move, merge, regroup-under-new-parent, rename and retire operations | **Organising the map** |
| `mindmap_link` | Adds a cross-link with a label and reason | Finding links across branches |
| `mindmap_claim` | Claims or releases a frontier node: shows presence on the canvas and stops swarm agents from doing the same node twice | Exploration and parallel agents |
| `mindmap_spawn` | Requests sub-agents on nodes (profile and template), within the run's budget | **Recursive exploration**: a node can grow its own research team |
| `mindmap_ask_human` | Creates a question node assigned to you. Your answer is sent back to the agent through `Messages().Send` | Human-in-the-loop without blocking |
| `mindmap_write_doc` | Creates or updates doc nodes, as a tree of docs | Compiling |
| `mindmap_report` | Writes the final run summary on the node and sets its status to review or done | Clean hand-off |

**Phase 0 adjustments:**
- Tool local names drop the `mindmap_` prefix (`context`, `read`, `add`, and so on), because Kandev already exposes them as `kandev_kandev_plugin_mindmap_<name>`.
- Launch prompts tell agents to load the tool schemas first. Tools return clear errors when required arguments are missing.
- The plugin receives the task id but not task metadata, so it keeps its own `task_id → run` table for access checks.

### Agent bridge (Pi and any MCP harness)
Pi has no MCP client, so Kandev's plugin tools never reach it. The `bridge/` package in this repo exposes the **same tool set** in two forms:
- a **Pi extension** that registers the tools natively in Pi;
- a standalone **stdio MCP server**, `kandev-mindmap-mcp`, that any MCP harness (Claude Code outside Kandev, Codex and others) can install.

Both call the plugin's loopback HTTP API `/agent/v1/tools/<name>` using a **run token**:
- The token is issued per run and injected into the launch prompt. A saved personal token is also allowed for use outside Kandev.
- The backend shares one tool implementation across Kandev MCP, the bridge and the prompt fallback.

**Prompt fallback:** for harnesses with neither, the launch prompt contains the outline and asks for a fenced `mindmap-patch` JSON block, which the backend parses from `message.added`. It always stays available.

## Bold, bleeding-edge features
1. **Apply vs propose.** Each run is either `apply` (writes go live) or `propose`. In propose mode, every write becomes a ghost layer on the canvas: dashed nodes, and arrows for moves and merges. You accept or reject it as a whole or node by node. Organiser runs default to propose.
2. **Lens swarms.** "Swarm this node" starts N agents with different lenses (Researcher, Skeptic, Builder, Contrarian, Examples-hunter, Futurist). Each claims its own sub-branch. You can mix harnesses, for example Claude for synthesis and pi or Cursor for research.
3. **Autonomous explorer.** You give a node a budget (max depth, nodes and sub-agents) and a goal. The explorer claims frontier nodes, expands them, spawns sub-agents, asks you questions, and stops at the budget. Its progress is visible live on the canvas.
4. **Gardener.** An organiser agent that removes duplicates, clusters, renames, adds missing cross-links and flags weak or uncited claims. It always runs as a proposal.
5. **Evidence-aware map.** Nodes show source counts and confidence. A "Critique" template adds linked counter-arguments and marks claims as contested. Uncited research is visually de-emphasised.
6. **Agent presence.** Claimed or running nodes show a pulsing ring with the harness icon. Nodes an agent just wrote to briefly highlight. The inspector shows the live agent stream.
7. **Time travel.** Built on the op log: undo/redo, a "replay how this idea grew" slider, and **fork map** to try a different direction.
8. **Execute.** A leaf `action` node becomes an implementation task in a repo, and its status links back to the node.
9. **Quick suggest.** A one-shot "suggest branches" (`InvokeUtilityAgent`) for fast ideas without starting a full task.

## Compile to documents
The map can be large, so compiling is a **map-reduce over the tree**:
1. One section agent per top-level branch, running in parallel as a swarm. Each reads its subtree and writes a section doc, splitting into sub-docs when a branch is large (about 8 children or more than about 1,500 words).
2. A synthesis agent writes the overview and an **`index.md`**, then a single consolidated **`<slug>.md`** you can read from start to finish.

Output:
- **Always:** a tree of `doc` nodes in the map, viewable in the inspector.
- **On disk:**
  ```
  <slug>.md                      single readable doc
  <slug>/index.md
  <slug>/<branch>/index.md
  <slug>/<branch>/<sub-branch>.md
  <slug>/assets/
  ```
  The docs cross-link to each other and keep source citations.
- **Location:**
  - If the map is linked to a repo, the compile task writes to `docs/mindmaps/` on its own branch (PR-able).
  - Otherwise the backend writes to the `export_dir` setting (default `~/Kandev/mindmaps/`), creating the folder if needed.
  - The UI also offers a zip or markdown download.

## Collaboration (multiplayer: several humans and several agents)
**Goal:** the map runs on your laptop, and one click makes it **public** through a tunnel with a secret path. Anyone with the link has the **same capabilities as you**, including starting agents.

**How it works:**
- **Plugin server.** The Go backend starts an HTTP + WS server on `127.0.0.1:<port>`. The port is chosen randomly at first start and saved in settings. The server provides:
  - `GET /m/<token>/`: the standalone web app (the same canvas code, built a second time with its own React);
  - `WS /m/<token>/ws`: ops, presence and agent output;
  - `/m/<token>/api/*`: run, swarm, compile, asset upload, agent-profile list and similar.

  The in-Kandev UI uses the same server through a local token issued by an authenticated Kandev webhook, so local and remote share one code path.
- **Going public.** You click **Share** on a map, and the plugin starts a tunnel subprocess:
  - The default is a `cloudflared` quick tunnel: no account, and a random `*.trycloudflare.com` hostname.
  - Tailscale Funnel or ngrok can be chosen in settings.
  - The share link is `https://<random-host>/m/<256-bit token>/`, where the token is the secret path.
  - The UI shows the link with copy and QR buttons, plus **Rotate link** and **Stop sharing**. Stopping kills the tunnel and invalidates the token.
  - If `cloudflared` isn't installed, the UI shows the `brew install cloudflared` command for you to run.
- **Scope.** A link grants access to **that one map only**. It gives no access to other maps, to Kandev's UI or API, or to other tasks. Guests never reach port 38430. Every agent action goes through the plugin backend → Host API, under your Kandev identity.
- **Identity.** Guests type a display name and get a colour. The browser keeps the session in a cookie. There are no accounts. Every op, run and proposal is recorded against the human or agent who made it.
- **Real-time model:**
  - The server is authoritative, with a per-map op log. Edits are field-level last-writer-wins.
  - Tree moves are checked on the server, which rejects cycles. Conflicting ops are rebased and the client is corrected.
  - **Presence:** live cursors, selection, and "Alice is editing…" soft locks on node bodies. Agents appear as participants with a harness icon.
  - The op log already handles undo per author, replay and attribution.
  - CRDT (Yjs) text editing inside node bodies is left as a later upgrade if soft locks feel limiting.
- **Agents for guests.**
  - Guests get the same Run / Swarm / Explore / Garden / Compile menus. The agent-profile list comes from `AgentProfiles().List` through the backend.
  - Live agent output comes from `OnEvent(message.added, agent.stream.*)` through the plugin WS, since guests can't use Kandev's conversation hooks.
  - `ask_human` questions can be answered by anyone, and the answer is recorded against that person.
- **Safety rails.** You asked that guests get the same powers, so nothing is gated by default. Remember that guest-started agents run on your machine with your keys.
  - **Host kill switch:** stop all runs and sharing.
  - Per-map concurrency and budget caps on runs (a setting).
  - Rate limits per guest session.
  - An audit list of who started what.
  - Asset uploads limited by size and type.
  - Tokens compared in constant time.
  - An optional "require my approval for guest runs" toggle, off by default.

## Agent activity visibility
Agents are first-class participants, like human collaborators. Each run has a colour and a harness icon (Claude, Cursor, pi). You should be able to tell at a glance which agents are working, where, and what they need from you.

**On the canvas:**
- Node status rings:
  - queued: dashed
  - running: pulsing, in the agent's colour, with a harness badge; several agents on one node show as stacked avatars
  - waiting on you: amber with a "?"
  - review: a badge with the number of proposed changes
  - failed: red
  - done: a check that fades out
- **Branch halo:** the subtree an agent has claimed gets a soft tint in its colour, so you can see its territory.
- **Write trail:** nodes an agent just created or changed flash in its colour and keep a small "new" dot until you've seen them. The edges into new children animate briefly.
- **Off-screen markers:** arrows at the edge of the viewport point to agents working outside the view. Clicking one flies the camera there.
- **Minimap:** shows coloured dots for active agents.
- **Follow mode:** "Follow this agent" keeps the camera on its latest edit.

**Agents dock:**
- A floating action button in the bottom-right, with a live count badge that pulses while agents run.
- It opens a **drawer** listing every run, grouped as a tree for swarms and explorers (parent run → sub-agents). Each row shows:
  - harness, lens or template, the node path (click to fly there), status and elapsed time;
  - budget used (nodes added out of the maximum, sub-agents out of the maximum);
  - the latest line of agent output, live.
- Row actions: open task, message the agent, review the proposal, answer its question, stop.
- The drawer also has an **activity feed**, for example "Skeptic (Claude) added 3 nodes under *Pricing*".

**In the map header:** a summary chip such as "3 agents running · 1 needs you". Clicking it opens the drawer, filtered to what needs you.

Guests see exactly the same thing. Humans also appear in the dock's participant list, so everyone can see who is in the map, human or agent.

## UI
- A `/mindmap` route and nav item, with a map list and the canvas.
- Keyboard-first editing in XMind style:
  - Tab adds a child, Enter adds a sibling, typing or F2 edits, arrows navigate, Space collapses, ⌘Z undoes.
  - ⌘K opens a command palette (run agent, swarm, explore, garden, compile, search).
- The inspector shows:
  - the markdown body, sources and assets;
  - the linked run's live agent output;
  - pending proposals with accept/reject.
- The node context menu has Run agent… (profile, template, mode, budget, optional repo), Swarm, Explore, Suggest, Compile branch, Execute and Open task.
- A task panel inside node-launched tasks shows the branch and links back.
- Performance targets:
  - 1,000+ nodes at 60 fps (offscreen virtualisation, rAF-batched layout, incremental relayout).
  - A keystroke shows within one frame.
  - Theming uses only host CSS variables.

## Execution: I direct, subagents build
I won't write code. I'll brief agents, review what they hand back, and post updates after each milestone. Agents work on feature branches and open PRs, because a hook blocks pushing to main. I'll ask you before merging.

**First I write the shared contract** (`contract/`: Go and TS types, op schema, webhook and tool schemas) so the agents can work in parallel.

| Phase | Work | Agent / model |
|---|---|---|
| 0. Scaffold + spike | Create the repo from the official template, set up build, packaging and CI, and build a hello-world page plus a `mindmap_ping` tool. Install it locally and check:<br>• the tool is visible from Claude, Cursor and pi tasks;<br>• the maximum tool argument size (for base64 screenshots);<br>• the plugin process can **listen on its own port and spawn `cloudflared`**;<br>• the in-Kandev UI can open a WS to `127.0.0.1:<port>`;<br>• whether the Host API can **stop a running task or session** (if not, "stop" messages the agent and links to the task);<br>• how long the long-poll fallback can be held. | Sonnet |
| 1a. Backend core | SQLite, op log and revs, proposals, plugin HTTP/WS server, webhooks and fallback long-poll, assets, all 13 tools with access checks, Go tests | Opus |
| 1b. Canvas UI | Layout, renderer, pan/zoom, keyboard editing, optimistic ops and WS sync, presence rendering, ghost proposal layer, host-adapter split (Kandev bundle and standalone build), vitest | Opus (parallel with 1a) |
| 2a. Runs + swarms + explorer | Run, swarm and explore endpoints, templates and lenses, budgets, `OnEvent` status, presence, `ask_human` round trip, task panel | Opus |
| 2b. Inspector + evidence + time travel + agent visibility | Sources and assets view, live output, replay slider, fork map; status rings, branch halos, write trails, off-screen markers, minimap, follow mode, agents dock and drawer, activity feed, header chip | Sonnet (inspector, time travel) + Opus (agent visibility, canvas-heavy) |
| 2c. Compile + export + suggest | Map-reduce compile, doc tree view, disk and repo export, zip, quick suggest | Sonnet |
| 2d. Sharing + multiplayer | Tunnel manager (cloudflared, with Tailscale and ngrok options), share/rotate/stop UI, guest identity, presence and soft locks, guest agent API and output streaming, safety rails, plus a **security review** of the token and scope model | Opus (build) + Opus (separate security review) |
| 2e. Agent bridge | `bridge/` package: Pi extension plus stdio MCP server over the loopback tool API; run tokens; prompt-fallback patch parser; install docs. Test with a real Pi run. | Sonnet |
| 3. Verify | QA in local Kandev with preview tools (below), plus an Opus code review of the full diff | Sonnet (QA) + Opus (review) |
| Chores | README, version bumps, release tarball | Haiku |

If the spike shows that an agent (for example pi or Cursor) doesn't get Kandev MCP tools, I'll stop and tell you. The fallback for that agent is a launch prompt containing the outline, plus a JSON patch block in its reply that the backend parses.

## Verification
- `make test` (Go `-race` plus vitest) and `make package-host`, then curl-install into Kandev on `127.0.0.1:38430` and reload.
- With preview tools on `/mindmap`:
  - keyboard-build a 3-level map;
  - refresh to check it persisted;
  - open a second tab to check sync.
- Real runs:
  - A Claude research run adds cited nodes live and attaches a page screenshot; status moves running → review.
  - A pi or Cursor run does the same where its tools work.
  - A 3-lens swarm shows claims without duplicate work.
  - While the swarm runs, the dock count, grouped drawer rows, branch halos, write trails, off-screen markers and follow mode all behave as described. A screenshot goes to you as proof.
  - The explorer stops at its budget.
  - `ask_human` round trip.
  - A gardener proposal shows as ghosts; accepting it applies, rejecting it reverts.
- Compile a 3-branch map and check the doc nodes, `<slug>.md`, the split folder and the export in both repo and non-repo modes.
- Check the replay slider and fork map.
- Multiplayer:
  - Click Share and open the tunnel URL in a separate browser profile as a guest. Check that you see each other's cursors and edits live, and that a guest can start a Claude run whose output streams to both.
  - Check that the link without the token, or with a wrong one, returns 404, and that it can't reach other maps or Kandev.
  - Rotate and stop sharing invalidate the old link.
  - The kill switch stops runs.
- Perf: seed a 1,000-node map and confirm pan/zoom and editing stay smooth.
