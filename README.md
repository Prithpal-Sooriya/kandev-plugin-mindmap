# kandev-plugin-mindmap

An agentic, multiplayer mind map for [Kandev](https://github.com/kdlbs/kandev).

Status: Phase 0 plus the shared contract. See [docs/spike-findings.md](docs/spike-findings.md)
for what the spike verified and [docs/protocol.md](docs/protocol.md) for the wire contract.

## Contract layout

| Path | What |
|---|---|
| `server/contract/` | Go types, op union, validation, tree checks, order keys, run status rules |
| `ui/src/contract/` | TS mirror (runtime shapes + inferred types), shared by the Kandev bundle and guest app |
| `contract/fixtures/` | JSON fixtures both test suites parse; they keep Go and TS in lockstep |
| `contract/tools/` | the 13 agent tool declarations; `npm run sync:tools` regenerates `manifest.yaml` |
| `server/tools/` | tool dispatcher (stubbed: validates arguments, returns `not_implemented`) |
| `server/store/schema.sql` | SQLite migration v1 |

## Develop

Needs a sibling checkout of the Kandev monorepo at `../kandev-sdk` at the commit
in `.kandev-sdk-ref` (the SDK is not published as its own module yet).

```sh
make test          # go test -race, tsc, vitest
make package-host  # builds kandev-plugin-mindmap-<version>.tar.gz for this platform
curl -F package=@kandev-plugin-mindmap-0.0.1.tar.gz http://127.0.0.1:38430/api/plugins/install
```

The backend is pure Go (`CGO_ENABLED=0`), including `modernc.org/sqlite`.
