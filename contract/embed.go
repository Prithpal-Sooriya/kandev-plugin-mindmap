// Package contractfiles embeds the agent tool declarations so the backend
// can serve them on GET /agent/v1/tools without bundling copies.
package contractfiles

import (
	"embed"
	"encoding/json"
	"fmt"
)

//go:embed tools/*.json
var toolFiles embed.FS

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
