package contractfiles

import (
	"strings"
	"testing"
)

func TestBridgePrompt(t *testing.T) {
	p := BridgePrompt()
	for _, want := range []string{"{{RUN_TOKEN}}", "run_token", "mindmap-patch", "mindmap_context"} {
		if !strings.Contains(p, want) {
			t.Errorf("bridge prompt lacks %q", want)
		}
	}
}
