// Package contractfiles embeds the agent tool declarations (and the bridge
// launch-prompt snippet) so the backend can serve them without bundling copies.
package contractfiles

import (
	"embed"
	"encoding/json"
	"fmt"
)

//go:embed tools/*.json
var toolFiles embed.FS

//go:embed prompts/*.md
var promptFiles embed.FS

// BridgePrompt returns the launch-prompt snippet for bridge runs (contract/prompts/bridge.md).
// It holds the {{RUN_TOKEN}} placeholder, which the launcher substitutes.
func BridgePrompt() string {
	b, err := promptFiles.ReadFile("prompts/bridge.md")
	if err != nil {
		panic(err) // embedded at build time
	}
	return string(b)
}

// ToolJSON returns the raw declaration of one tool (contract/tools/<name>.json).
func ToolJSON(name string) ([]byte, error) { return toolFiles.ReadFile("tools/" + name + ".json") }

// Tools decodes every declaration, in the given order, into T.
func Tools[T any](order []string) ([]T, error) {
	out := make([]T, 0, len(order))
	for _, n := range order {
		b, err := ToolJSON(n)
		if err != nil {
			return nil, err
		}
		var t T
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, fmt.Errorf("contract/tools/%s.json: %w", n, err)
		}
		out = append(out, t)
	}
	return out, nil
}
