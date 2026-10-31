// Package contract is the shared wire contract for the Mind Map plugin.
//
// It defines the JSON shapes exchanged between the Go backend, the canvas UI
// (in-Kandev bundle and standalone guest app) and the agent bridge (Pi
// extension and stdio MCP server). The TypeScript mirror lives in
// ui/src/contract and must produce byte-for-byte compatible JSON. Both sides
// are checked against the shared fixtures in contract/fixtures.
//
// Conventions:
//   - Field names are snake_case.
//   - Optional fields are omitted when empty (Go `omitempty`, TS `?`). The
//     contract never sends JSON null; clearing a field uses an explicit
//     `clear` list (see NodeUpdatePayload).
//   - Timestamps are RFC 3339 strings in UTC (`time.Time` in Go, `string` in TS).
//   - IDs are opaque strings with a type prefix (see ids.go). Clients may mint
//     node, link and op IDs so optimistic edits can reference them before the
//     server acknowledges.
//
// Everything in this package is pure: no I/O, no clocks, no globals that
// change at runtime. Validation helpers take the state they need as arguments.
package contract
