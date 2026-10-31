# Build handoff (for resuming in a later session)

How this is built: the build moved from Claude Code to Cursor on 2026-10-31. A Cursor director session spawns subagents with a mix of models (Claude Opus for core and security, Gemini 3.1 Pro and Grok for features, a fast model for chores). The user has authorised the director to create and merge PRs. Every change lands as a PR to main. A hook blocks direct pushes to main. The full plan is in `docs/PLAN.md`, the spike results in `docs/spike-findings.md`, and the wire contract in `docs/protocol.md`.

## Status (2026-10-31)

| Phase | State | Where |
|---|---|---|
| 0. Scaffold + spike | Merged | PR #1 |
| CI workflows | Merged | PR #2 |
| Shared contract (types, 13 tool schemas, protocol, SQLite schema) | Merged | PR #3 |
| 1a. Backend core (store, op engine, loopback HTTP/WS server, webhooks, 13 tools) | Merged | PR #4 |
| Plan and handoff docs | Merged | PR #5 |
| 2e. Agent bridge (Pi extension + stdio MCP server) and prompt fallback | Merged | PR #6 |
| 1b. Canvas UI (layout, rendering, keyboard, presence, ghost proposals, host-adapter split, standalone build) | Merged | PR #7 |
| 2a. Runs, swarms, explorer, task launch (`Launcher` impl), ask_human round-trip, task panel | In progress | branch `phase2a-runs-v2` |
| 2b. Inspector, evidence, time travel, agent visibility dock/drawer | In progress | branch `phase2b-visibility-v2` |
| 2d. Sharing, guest identity and safety rails | In progress | branch `phase2d-sharing` |
| 2c. Compile (map-reduce), export, quick suggest | Not started | starts after 2a merges |
| 2d. Security review | Not started | |
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

- Agents work in sibling worktrees `../mm-<phase>` created from `origin/main`.
- Check the open PRs and branches (`gh pr list`, `git branch -a`).
- If a phase 1 branch exists but has no PR, look at its last commits to see how far it got before starting a new agent to continue it.
- Phase 2 workstreams depend on Phase 1a and 1b being merged.
