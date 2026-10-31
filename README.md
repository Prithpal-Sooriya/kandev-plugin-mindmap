# kandev-plugin-mindmap

An agentic, multiplayer mind map for [Kandev](https://github.com/kdlbs/kandev).

Status: Phase 1a (backend core) and 1b (canvas UI). See [docs/protocol.md](docs/protocol.md) for the wire contract
(§13 lists what the backend implements so far) and [docs/spike-findings.md](docs/spike-findings.md)
for what the Phase 0 spike verified.

## Contract layout

| Path | What |
|---|---|
| `server/contract/` | Go types, op union, validation, tree checks, order keys, run status rules |
| `ui/src/contract/` | TS mirror (runtime shapes + inferred types), shared by the Kandev bundle and guest app |
| `contract/fixtures/` | JSON fixtures both test suites parse; they keep Go and TS in lockstep |
| `contract/tools/` | the 13 agent tool declarations; `npm run sync:tools` regenerates `manifest.yaml` |
| `server/store/` | SQLite store: op log, apply engine, proposals, runs, tokens, assets (`schema.sql` is migration v1) |
| `server/hub/` | per-map fan-out to WS clients and the long-poll ring buffer; presence and soft locks |
| `server/app/` | the core service: commit + broadcast, runs (behind the `Launcher` interface), output, activity |
| `server/tools/` | the 13 agent tools, shared by Kandev MCP, the bridge API and `mindmap-patch` blocks |
| `server/web/` | loopback HTTP + WS server (`/m/<token>/...`, `/agent/v1/...`); embeds `server/web/dist` |
| `ui/src/canvas/` | the canvas (layout, cards, pan/zoom, keyboard, presence, ghosts, inspector, palette), host-agnostic |
| `ui/src/core/`, `ui/src/transport/` | op reducer, optimistic sync engine, undo; WS client with long-poll fallback |
| `ui/src/kandev/` | in-Kandev bundle entry (`ui/bundle.js`): host React via the registry API, webhooks |
| `ui/src/standalone/` | guest web app entry (own React 19), built into `server/web/dist/` |
| `ui/src/fake/`, `ui/dev/` | in-memory protocol server for tests, and a dev server with simulated participants |

## Develop

Needs a sibling checkout of the Kandev monorepo at `../kandev-sdk` at the commit
in `.kandev-sdk-ref` (the SDK is not published as its own module yet).

```sh
make test          # go test -race, tsc, vitest
make package-host  # builds kandev-plugin-mindmap-<version>.tar.gz for this platform
curl -F package=@kandev-plugin-mindmap-0.1.0.tar.gz http://127.0.0.1:38430/api/plugins/install
```

UI development without Kandev or the Go backend:

```sh
npm run dev        # http://127.0.0.1:5178/ : standalone app against the in-memory fake server
                   # /m/mms1.SSS…/ guest view · /kandev/ the in-Kandev bundle under a mock host
                   # ?seed=1000&perf=1 seeds a 1,000-node map with an fps overlay · ?ws=0 forces long-poll
npm run build:ui   # typecheck + ui/bundle.js + server/web/dist/
```

The backend is pure Go (`CGO_ENABLED=0`), including `modernc.org/sqlite`.
