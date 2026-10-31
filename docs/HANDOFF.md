# Build handoff (for resuming in a later session)

How this is built: a "director" Claude session plans and reviews, while subagents do the work on feature branches, with the model chosen per task (Opus for core, Sonnet for features and QA, Haiku for chores). Every change lands as a PR to main, and the user approves each merge. A hook blocks direct pushes to main. The full plan is in `docs/PLAN.md`, the spike results in `docs/spike-findings.md`, and the wire contract in `docs/protocol.md`.

## Status (2026-10-31)

| Phase | State | Where |
|---|---|---|
| 0. Scaffold + spike | Merged | PR #1 |
| CI workflows | Merged | PR #2 |
| Shared contract (types, 13 tool schemas, protocol, SQLite schema) | Merged | PR #3 |
| 1a. Backend core (store, op engine, loopback HTTP/WS server, webhooks, 13 tools) | In progress | branch `phase1a-backend` |
| 1b. Canvas UI (layout, rendering, keyboard, presence, ghost proposals, host-adapter split, standalone build) | In progress | branch `phase1b-canvas` |
| 2a. Runs, swarms, explorer, task launch (`Launcher` impl), ask_human round-trip, task panel | Not started | |
| 2b. Inspector, evidence, time travel, agent visibility dock/drawer | Not started | |
| 2c. Compile (map-reduce), export, quick suggest | Not started | |
| 2d. Sharing (cloudflared tunnel), multiplayer, safety rails, security review | Not started | |
| 2e. Agent bridge (Pi extension + stdio MCP server) and prompt fallback | Not started | |
| 3. Verification in local Kandev + code review | Not started | |

## Key facts learned

- Claude and Cursor tasks see the plugin's tools, for example `mcp__kandev__kandev_kandev_plugin_mindmap_<tool>`. Pi has no MCP client, so it needs the bridge (2e) or the prompt fallback.
- Claude defers MCP tool schemas, so launch prompts must tell it to load the tool schemas first.
- There is no Host API to stop a session. "Stop" means messaging the agent and revoking its run token.
- The server write timeout is 30 s, so long-polls are held for at most 20 s.
- The desktop CSP blocks images served from loopback, so assets go through the `asset` webhook.
- Run status comes from `task_session.state_changed`, because `agent.completed` and `agent.failed` don't fire.
- cloudflared is not installed on the user's machine. Installing it is the user's call.
- Local Kandev runs at http://127.0.0.1:38430. Install with: `make package-host`, then `curl -F package=@<tar.gz> http://127.0.0.1:38430/api/plugins/install`.

## Resuming

- Check the open PRs and branches (`gh pr list`, `git branch -a`).
- If a phase 1 branch exists but has no PR, look at its last commits to see how far it got before starting a new agent to continue it.
- Phase 2 workstreams depend on Phase 1a and 1b being merged.
