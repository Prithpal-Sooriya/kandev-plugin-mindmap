# Phase 0 spike findings

Environment: Kandev desktop app on `127.0.0.1:38430`, SDK checkout pinned in `.kandev-sdk-ref`
(`570600439036e81f8e9e1c63f15c4abce8a6c846`), plugin `kandev-plugin-mindmap` 0.0.1, macOS arm64, Go 1.27.
SDK paths below are relative to `kandev-sdk/apps/backend`.

Spike code: `server/plugin.go` (tool, events), `server/webhooks.go` (debug webhooks),
`server/probe_server.go` (loopback HTTP+WS, sqlite probe), `ui/src/register.tsx` (hello page).

## Summary

| # | Check | Result |
|---|-------|--------|
| 1 | Agent tools reach real agents | Claude PASS (with caveat), Cursor PASS, Pi FAIL (no MCP adapter in this install) |
| 2 | Max tool argument size | No explicit cap below ~16 MiB; 1 MiB applies to results only. Practical limit is model output. |
| 3 | Plugin process can listen and spawn | PASS. cloudflared not installed, so no tunnel test. |
| 4 | UI to loopback WebSocket | PASS by code and headers. Not exercised in a live browser (see below). |
| 5 | Host API can stop a session | No direct call. Only task delete (needs human confirmation) or archive via REST. |
| 6 | Long-poll hold time | 30 s hard ceiling (measured). Use 20-25 s. |
| 7 | Other blockers | sqlite builds with CGO_ENABLED=0 (PASS). OnEvent receives all four events (PASS). |

## 1. Agent tools reach real agents

Launch path used: `POST /api/plugins/kandev-plugin-mindmap/webhooks/debug-launch` ->
`Host.Tasks().Create` with `StartAgent: true`, `Launch{AgentProfileID, Prompt}` and `Metadata{mindmap_id,node_id,run_id}`
(`server/webhooks.go:109-140`). Works with only `api_write: tasks`. Task lands in the default workspace/workflow, no repo needed.

Prompt: `Call the kandev mindmap_ping tool with {"msg":"hello"} and reply with its output. Do nothing else.`
Evidence: `GET .../webhooks/logs?name=ping` (file `ping.log` in `KANDEV_PLUGIN_DATA_DIR`) and the session messages API.

| Agent | Tool visible | Called | Exact name the agent saw | Notes |
|-------|--------------|--------|--------------------------|-------|
| Claude Code | yes | yes | `mcp__kandev__kandev_kandev_plugin_mindmap_mindmap_ping` | First run sent `args: {}`: the agent said the tool schema was not loaded (Claude Code defers MCP tool schemas). A retry whose prompt said to load the schema first sent `{"msg":"hello"}` correctly. |
| Cursor | yes | yes | `kandev_kandev_plugin_mindmap_mindmap_ping` (server `kandev`) | Raised a permission request, auto-approved by Kandev (`source: auto_approve`). Args `{"msg":"hello"}` received. |
| Pi | no | no | n/a | Agent replied that no kandev tool exists in the harness. |

The tool name is `kandev_` + plugin id (`kandev-plugin-mindmap` becomes `kandev_plugin_mindmap`) + `_` + local name. Since the
plugin id already starts with `kandev`, the name stutters (`kandev_kandev_plugin_mindmap_mindmap_ping`). Local tool names should not
repeat the `mindmap_` prefix if we want shorter names, but prompts should refer to the tool by its suffix.

Context received by the plugin per call: `task_id`, `session_id`, `workspace_id`, `surface=kanban-task`, `invocation_id`
(`server/plugin.go:63-90`; `internal/mcp/handlers/plugin_tools.go:76-97`). Task metadata is not included, so access checks must look the task up
(`Tasks().Get`) or keep a task-to-run map written at launch.

Pi cause: Kandev does write a project-local `.pi/mcp.json` with the `kandev` server into the Pi task workspace
(`internal/agent/mcpconfig/passthrough.go:336-385`, `internal/agent/agents/pi_acp.go:104`), but Pi has no built-in MCP client. It needs an
MCP extension that reads that file, and none is installed here (`~/.pi/agent/extensions` has no MCP adapter). Not installed as part of the spike.
Other Pi profiles share the same adapter, so they would behave the same.

Cleanup: `PluginOwnedTaskTrees().Delete` returned `PermissionDenied native_human_confirmation_required`, so plugins cannot delete tasks.
The four spike tasks were archived with `POST /api/v1/tasks/:id/archive` instead.

Implications: Claude and Cursor are fine. Pi needs either a user-installed MCP adapter or the fallback (outline in the prompt plus a JSON patch
block in the reply). Prompts for Claude should tell it to load the tool schemas first, and tools should tolerate or reject missing required args with a clear error.

## 2. Maximum tool argument size

From code, the chain is agent -> agentctl MCP server -> backend WS -> plugin gRPC:

- Manifest `input_schema` is capped at 64 KiB serialized (the schema, not the arguments). Unknown top-level args are rejected
  (`internal/plugins/agent_tools.go:compileInvocationSchema`). A `maxLength` in the schema is the only per-argument cap.
- No `SetReadLimit` on the agentctl-to-backend path (`grep SetReadLimit` finds only LSP and the browser gateway). The browser gateway limit is 32 MiB
  (`internal/gateway/websocket/client.go:30`) and is not on this path.
- Plugin gRPC server: `grpc.MaxRecvMsgSize(17<<20)` (`pkg/pluginsdk/serve.go:420`).
- Results: text plus structured content at most 1 MiB, and text must be non-empty (`internal/plugins/agent_tools.go:20,160-170`).
- Per-call plugin deadline: 30 s (`internal/plugins/agent_tools.go:211`).

Tested the gRPC leg via the `echo-size` webhook: 4 MiB and 15 MiB bodies arrive intact (HTTP 200); 17 MiB is rejected by the 16 MiB
webhook cap with 413. An agent cannot cheaply emit multi-MB tool args, so the end-to-end agent test was not run.

Implication: a base64 screenshot in a tool argument is limited by the model's output tokens, not by Kandev. The `mindmap_attach` design
(URL or worktree path first, base64 only for small images) is right. Keep base64 under about 100 KB.

## 3. Plugin process can listen and spawn

`GET .../webhooks/probe` output:

```
listen_addr 127.0.0.1:52593, ws_url ws://127.0.0.1:52593/ws
subprocess.echo "subprocess-ok"
subprocess.cloudflared "not found"
sqlite.version "3.54.1", rows 1
```

- `curl http://127.0.0.1:52593/hello` returns `hello from kandev-plugin-mindmap`.
- WebSocket upgrade with `Origin: http://127.0.0.1:38430` returns `101 Switching Protocols`.
- `which cloudflared`: not found. Nothing was installed and no tunnel was started. The `tunnel-test` webhook is in place and would run
  a 20 s quick tunnel once cloudflared exists.

Implication: the plugin can own a port and run subprocesses with no extra capability. The port changes on every plugin restart
(Kandev restarts the plugin on config save), so the real plugin must persist its chosen port and republish it through a webhook.

## 4. In-Kandev UI to `127.0.0.1:<port>` WebSocket

Hello page (`ui/src/register.tsx`) fetches the port from `/webhooks/probe` via `host.api.fetch`, then tries `fetch` and `new WebSocket` and renders the status.
It also mirrors results to `window.__mindmapProbe`.

Evidence (no live browser run: the preview tool refused to attach to the already-running server and Claude in Chrome was not connected):

- `curl -I http://127.0.0.1:38430/mindmap` has no `Content-Security-Policy` header, and the HTML has no CSP meta tag.
- The desktop shell (Tauri) sets a CSP in `apps/desktop/src-tauri/tauri.conf.json:35`:
  `connect-src 'self' http://127.0.0.1:* ws://127.0.0.1:*; img-src 'self' data:; script-src 'self' 'unsafe-eval'; frame-src 'self' http://127.0.0.1:* http://localhost:*`.
  So `ws://127.0.0.1:<any>` and `http://127.0.0.1:<any>` fetches are allowed.
- `http://` page to `ws://127.0.0.1` is not mixed content (loopback).

Gotchas for the plan: `img-src` is `'self' data:`, so `<img src="http://127.0.0.1:<port>/...">` is blocked in the desktop shell. Serve assets
through an authenticated Kandev webhook or as `data:` URIs. `script-src 'self'` means the plugin server cannot serve scripts into the Kandev page. The plugin server
should set `Access-Control-Allow-Origin` and check WS `Origin` itself.

To finish verifying: open `/mindmap` in Kandev and read `window.__mindmapProbe` (expect `ws: "open hello:<origin>"`).

## 5. Stopping a running session from the Host API

No stop or cancel call exists. `pluginsdk.Host` and its optional extensions expose no session stop (`pkg/pluginsdk/host.go`, `interactions.go`).
Available partial options:

- `Messages().Send` (api_write: messages): asks the agent to stop, not enforced.
- `Tasks().Update` with `State` (api_write: tasks): only sets the task field, does not stop the session.
- `PluginOwnedTaskTrees().Delete`: deletes the task and stops agents asynchronously (`internal/task/service/service_tasks.go:2917`), but in this build the call
  fails with `native_human_confirmation_required`.
- `CancelClarification` (`interactions.go:160`) only declines a pending clarification.

Implication: "Stop" in the UI should message the agent and link to the task. A real stop needs an SDK addition upstream.

## 6. Long-poll hold time

Code: `http.Server.WriteTimeout` = `server.writeTimeout`, default 30 s (`internal/common/config/catalog.go:62`, `internal/backendapp/httpserver.go:118`);
`ReadTimeout` default 30 s too. Webhook relay passes the request context to the plugin RPC with no extra deadline (`internal/plugins/invoke.go:56`).
Browser actions have a separate 15 s budget (`internal/plugins/handlers.go:48`).

Measured with `GET .../webhooks/hold?secs=N`: 20 s OK, 28 s OK, 29 s OK, 31 s and 35 s fail (curl gets an empty reply, HTTP 000, even though the plugin handler completes).

Implication: hold long-polls for at most 20-25 s and loop. Operators can raise `KANDEV_SERVER_WRITETIMEOUT`, so do not rely on it.

## 7. Other findings

- `modernc.org/sqlite` v1.61.1 builds with `CGO_ENABLED=0` (`go build` succeeds, `make package-host` uses it) and works at runtime (sqlite 3.54.1, write+read). Covered by `TestSQLiteProbeWorksWithoutCGO`.
- `OnEvent` received all four subscribed event types. Counts after four short tasks: `task_session.state_changed` 42, `message.added` 24. Neither `agent.completed` nor
  `agent.failed` appeared in these short runs (they may only fire on certain lifecycles), so run status should be derived from `task_session.state_changed` (`new_state` e.g. `RUNNING`, `WAITING_FOR_INPUT`).
- `Event.WorkspaceID` was empty on every delivery. Payload carries `task_id` and `session_id`. Resolve workspace via `Tasks().Get`.
- Some `task_session.state_changed` payloads lack `new_state`; read `new_state`/`old_state` defensively.
- A task created with `Launch` and no repository still gets a worktree-style workspace under `~/.kandev/tasks/<workspace>/<task>`.
- Authenticated webhooks work with no credentials against the local desktop instance; the real plugin must not assume that holds for remote Kandev.
- `curl -F package=@...` install of a new plugin id works and starts the process within seconds.
