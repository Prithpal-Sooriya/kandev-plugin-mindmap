# kandev-plugin-mindmap

An agentic, multiplayer mind map for [Kandev](https://github.com/kdlbs/kandev).

Status: Phase 1a (backend core). See [docs/protocol.md](docs/protocol.md) for the wire contract
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

## Develop

Needs a sibling checkout of the Kandev monorepo at `../kandev-sdk` at the commit
in `.kandev-sdk-ref` (the SDK is not published as its own module yet).

```sh
make test          # go test -race, tsc, vitest
make package-host  # builds kandev-plugin-mindmap-<version>.tar.gz for this platform
curl -F package=@kandev-plugin-mindmap-0.1.0.tar.gz http://127.0.0.1:38430/api/plugins/install
```

The backend is pure Go (`CGO_ENABLED=0`), including `modernc.org/sqlite`.
