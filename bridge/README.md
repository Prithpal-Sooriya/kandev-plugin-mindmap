# Agent bridge

Exposes the 13 mind map tools (`context`, `read`, `search`, `add`, `update`, `attach`, `restructure`, `link`, `claim`,
`spawn`, `ask_human`, `write_doc`, `report`) to agents that cannot use Kandev's own plugin tools. Both forms call the
plugin's loopback API, `POST http://127.0.0.1:<port>/agent/v1/tools/<name>` (docs/protocol.md §8.3), and expose the tools
as `mindmap_<name>` with the schemas from `contract/tools/*.json`, embedded at build time.

| Output | For |
|---|---|
| `dist/kandev-mindmap-mcp.mjs` | stdio MCP server (Claude Code outside Kandev, Codex, Cursor, Pi's built-in MCP client, ...) |
| `dist/pi-extension.mjs` | Pi extension that registers the tools natively with `pi.registerTool` |

Pi 1.4 registers tools natively (verified, see below). Its built-in MCP client also reads `mcp.json`, so either form works
there; the native extension has no extra process and no `mcp__` name prefix.

## Build and test

```sh
make bridge        # install, typecheck, build dist/
make test-bridge   # typecheck + vitest (also part of `make test`)
npm run live --prefix bridge   # live call against the plugin installed in local Kandev
```

Node 20.19 or newer. Each output is one file; `dist/` is git-ignored.

## Tokens

The bridge authenticates with `Authorization: Bearer <token>`. It looks for a token in this order:

1. `MINDMAP_TOKEN` environment variable.
2. Config file `~/.config/kandev-mindmap/config.json`: `{"token": "mmp1.<port>.<secret>"}` (`MINDMAP_CONFIG` changes the path).
3. A `run_token` argument on the call. The bridge adds this property to every tool schema **only when 1 and 2 give nothing**,
   and removes it before calling the API. This is how a run token from a launch prompt reaches an agent whose environment
   you do not control. See [PROMPT.md](PROMPT.md).

Token kinds: a **run token** (`mmr1.<port>.<secret>`) belongs to one run and map. A **personal token**
(`mmp1.<port>.<secret>`) acts as the host on all maps. With a personal token there is no run, so every call needs a
`map_id` (`read`) or a `node_id`/`parent_id` (most others); `context` returns the map list.

The token carries the plugin's loopback port, so no URL is needed. `MINDMAP_URL` overrides the base URL (for tests).

### Get a personal token

Call the `session` webhook, then mint the token with the local token it returns:

```sh
curl -s -X POST http://127.0.0.1:38430/api/plugins/kandev-plugin-mindmap/webhooks/session -d '{}'
# -> {"base_url": "http://127.0.0.1:<port>", "token": "mml1....", ...}
curl -s -X POST "<base_url>/m/<mml token>/api/tokens/personal" -d '{"label": "my bridge"}'
# -> {"id": "...", "token": "mmp1.<port>.<secret>", "label": "my bridge"}
```

Save it in the config file (mode 600) or export `MINDMAP_TOKEN`. The port inside the token is the plugin's loopback port. If the plugin ever has to move to a new port (the saved one was
taken on restart), calls fail with `cannot reach the Mind Map plugin`; mint a new token then.

## Install: MCP server (any MCP harness)

Build it, then point the harness at the file. Nothing else is needed at runtime except Node.

```sh
make bridge
```

Claude Code (project or user scope):

```sh
claude mcp add mindmap --env MINDMAP_TOKEN=mmp1.<port>.<secret> -- node /abs/path/to/bridge/dist/kandev-mindmap-mcp.mjs
```

Generic `mcp.json` (Cursor, Codex, Pi's `~/.pi/agent/mcp.json`, ...):

```json
{
  "mcpServers": {
    "mindmap": {
      "command": "node",
      "args": ["/abs/path/to/bridge/dist/kandev-mindmap-mcp.mjs"],
      "env": { "MINDMAP_TOKEN": "mmp1.<port>.<secret>" }
    }
  }
}
```

Leave `env` out and the tools gain a `run_token` property instead. In Pi the tools appear as
`mcp__mindmap__mindmap_<name>` and, with the default `codemode` exposure, are reached through scripts. Set
`"exposure": "direct"` on the server to declare them to the model like built-in tools.

## Install: Pi extension

Pi 1.4's `pi.registerTool` registers tools natively. The extension is a single file. **Nothing here installs it for you.**

Try it for one run (touches no config):

```sh
make bridge
MINDMAP_TOKEN=mmp1.<port>.<secret> pi -e /abs/path/to/bridge/dist/pi-extension.mjs
```

Install it for every Pi session by copying the file into Pi's extensions directory (Pi loads `.js` and `.ts` files there):

```sh
cp bridge/dist/pi-extension.mjs ~/.pi/agent/extensions/kandev-mindmap.js
```

Pi runs extensions in-process, so the same token rules apply: set `MINDMAP_TOKEN` or the config file in the environment
Pi starts in. Without a token the tools carry `run_token`. Tools are registered when Pi loads the extension and decide
whether to add `run_token` at that moment; run `/reload` after adding a config file.

To remove it, delete the file. Tool names in Pi: `mindmap_context`, `mindmap_read`, ... Tool errors reach the model as
Pi tool errors with the plugin's message (`error bad_request: depth: must be 1-10`).

## Error mapping

Tool errors come back from the plugin as HTTP 200 and pass through unchanged. Transport problems become one-line
`error <code>: ...` messages that say what to do:

| Problem | Message starts with |
|---|---|
| no or malformed token | `error unauthorized: no Mind Map token ...` / `invalid token from env: ...` |
| 401 | `error unauthorized: the plugin rejected the token ...` |
| 426 | `error version: ...` (contract mismatch) |
| 413 | `error too_large: ...` |
| 429 | `error rate_limited: ... retry in N ms` |
| 5xx | `error <code>: ... plugin-side problem` |
| connection refused | `error unavailable: cannot reach the Mind Map plugin at http://127.0.0.1:<port> ...` |
| no answer within 28 s | `error unavailable: ... read the map before retrying a write` |

## `attach` with relative paths

The plugin only reads absolute paths under Kandev's task directory or the temp dirs. For a **relative** `file_path` the
bridge reads the file itself (relative to the agent's working directory), infers the MIME type from the extension and
sends `base64` and `filename` (max 10 MB). Absolute paths and URLs are passed through.

## Layout

```
src/tools.ts       embedded declarations, run_token schema logic
src/token.ts       token lookup and parsing
src/client.ts      POST /agent/v1/tools/<name>, error mapping, attach handling
src/mcp-server.ts  MCP server (low-level Server; raw JSON Schema, no zod conversion)
src/mcp.ts         stdio entry
src/pi.ts          Pi extension entry
scripts/build.mjs  esbuild: two single-file outputs
scripts/live-test.mjs   live check against a running plugin
tests/             vitest against a fake HTTP server, plus an MCP client over stdio
```
