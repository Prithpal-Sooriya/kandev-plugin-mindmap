# kandev-plugin-mindmap

An agentic, multiplayer mind map for [Kandev](https://github.com/kdlbs/kandev).

Status: Phase 0. This repo is a scaffold plus capability probes. See
[docs/spike-findings.md](docs/spike-findings.md) for what the spike verified.

## Develop

Needs a sibling checkout of the Kandev monorepo at `../kandev-sdk` at the commit
in `.kandev-sdk-ref` (the SDK is not published as its own module yet).

```sh
make test          # go test -race, tsc, vitest
make package-host  # builds kandev-plugin-mindmap-<version>.tar.gz for this platform
curl -F package=@kandev-plugin-mindmap-0.0.1.tar.gz http://127.0.0.1:38430/api/plugins/install
```

The backend is pure Go (`CGO_ENABLED=0`), including `modernc.org/sqlite`.
