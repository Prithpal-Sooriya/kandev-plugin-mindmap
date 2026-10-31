package contract

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Prompt-fallback patch blocks. A harness with neither Kandev MCP nor the
// bridge writes fenced blocks in its chat reply:
//
//	```mindmap-patch
//	{"v":1,"calls":[{"tool":"add","args":{"nodes":[{"title":"..."}]}}]}
//	```
//
// Each call is exactly a tool call (same names and arguments as
// contract/tools), executed in order through the shared tool implementation
// with the run's identity. Refs set by earlier calls (or earlier blocks in the
// same run) resolve in later ones.

const PatchFence = "mindmap-patch"

// PatchVersion is the only accepted value of Patch.V.
const PatchVersion = 1

// MaxPatchCalls bounds the calls in one block.
const MaxPatchCalls = 50

type Patch struct {
	V     int         `json:"v"`
	Calls []PatchCall `json:"calls"`
}

type PatchCall struct {
	Tool ToolName        `json:"tool"`
	Args json.RawMessage `json:"args"`
}

// ExtractPatches finds every ```mindmap-patch fenced block in a chat message
// and decodes it. Blocks that fail to parse are reported in errs (with their
// 0-based block index) and skipped; good blocks are still returned. An
// unterminated fence at the end of the message is ignored (the agent may
// still be streaming).
func ExtractPatches(text string) (patches []Patch, errs []error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	block := -1
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trimmed, "```") || strings.TrimSpace(strings.TrimPrefix(trimmed, "```")) != PatchFence {
			continue
		}
		var body []string
		closed := false
		for i++; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "```" {
				closed = true
				break
			}
			body = append(body, lines[i])
		}
		if !closed {
			break
		}
		block++
		p, err := ParsePatch([]byte(strings.Join(body, "\n")))
		if err != nil {
			errs = append(errs, fmt.Errorf("mindmap-patch block %d: %w", block, err))
			continue
		}
		patches = append(patches, p)
	}
	return patches, errs
}

// ParsePatch decodes and checks one block body. It validates each call's
// arguments with DecodeToolArgs so errors name the call and field.
func ParsePatch(body []byte) (Patch, error) {
	var p Patch
	if err := StrictUnmarshal(body, &p); err != nil {
		return Patch{}, Errf(ErrBadRequest, "invalid JSON: %v", err)
	}
	if p.V != PatchVersion {
		return Patch{}, FieldErr(ErrVersion, "v", "expected v=%d", PatchVersion)
	}
	if len(p.Calls) == 0 || len(p.Calls) > MaxPatchCalls {
		return Patch{}, FieldErr(ErrBadRequest, "calls", "give 1-%d calls", MaxPatchCalls)
	}
	for i, c := range p.Calls {
		f := fmt.Sprintf("calls[%d]", i)
		if NewToolArgs(c.Tool) == nil {
			return Patch{}, FieldErr(ErrBadRequest, f+".tool", "unknown tool %q", c.Tool)
		}
		if !c.Tool.PatchAllowed() {
			return Patch{}, FieldErr(ErrBadRequest, f+".tool", "%s is read-only; the outline is already in your prompt", c.Tool)
		}
		args, err := DecodeToolArgs(c.Tool, c.Args)
		if err != nil {
			e := *AsError(err)
			e.Field = strings.TrimSuffix(f+".args."+e.Field, ".")
			return Patch{}, &e
		}
		if a, ok := args.(*AttachArgs); ok && a.URL == "" {
			return Patch{}, FieldErr(ErrBadRequest, f+".args.url", "attach in a patch block supports url only")
		}
	}
	return p, nil
}
