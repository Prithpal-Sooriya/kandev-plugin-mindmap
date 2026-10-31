package tools

import (
	"context"
	"strings"
	"testing"

	"kandev-plugin-mindmap/server/contract"
)

func TestStubValidatesThenReportsNotImplemented(t *testing.T) {
	d := NewStub()
	ctx := context.Background()
	c := Caller{Transport: contract.TransportMCP, TaskID: "task-1"}

	for _, name := range contract.ToolNames {
		res := d.Invoke(ctx, c, string(name), validArgs[name])
		if !res.IsError || !strings.HasPrefix(res.Text, "error not_implemented:") {
			t.Errorf("%s: want not_implemented, got %+v", name, res)
		}
	}

	res := d.Invoke(ctx, c, "add", []byte(`{}`))
	if !res.IsError || !strings.Contains(res.Text, "bad_request") || !strings.Contains(res.Text, "nodes") {
		t.Errorf("missing required arg: got %q", res.Text)
	}
	res = d.Invoke(ctx, c, "add", []byte(`{"nodes":[{"title":"x"}],"bogus":1}`))
	if !res.IsError || !strings.Contains(res.Text, "bad_request") {
		t.Errorf("unknown arg: got %q", res.Text)
	}
	res = d.Invoke(ctx, c, "ping", nil)
	if !res.IsError || !strings.Contains(res.Text, "not_found") {
		t.Errorf("unknown tool: got %q", res.Text)
	}
}

func TestRegisterOverridesStub(t *testing.T) {
	d := NewStub()
	d.Register(contract.ToolContext, func(context.Context, Caller, any) (contract.ToolResult, error) {
		return contract.ToolResult{}, nil
	})
	res := d.InvokeMap(context.Background(), Caller{}, "context", nil)
	if res.IsError || res.Text != "ok" {
		t.Fatalf("got %+v", res)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("Register of unknown tool should panic")
		}
	}()
	d.Register("nope", nil)
}

var validArgs = map[contract.ToolName][]byte{
	contract.ToolContext:     []byte(`{}`),
	contract.ToolRead:        []byte(`{"depth":2}`),
	contract.ToolSearch:      []byte(`{"query":"pricing"}`),
	contract.ToolAdd:         []byte(`{"nodes":[{"title":"Usage-based pricing","children":[{"title":"Example","kind":"example","sources":[{"url":"https://example.com/a"}]}]}]}`),
	contract.ToolUpdate:      []byte(`{"node_id":"nd_0000000000000001","append_body":"More."}`),
	contract.ToolAttach:      []byte(`{"node_id":"@shot","url":"https://example.com/a.png"}`),
	contract.ToolRestructure: []byte(`{"reason":"tidy","ops":[{"op":"rename","node_id":"nd_0000000000000001","title":"New"}]}`),
	contract.ToolLink:        []byte(`{"from_id":"nd_0000000000000001","to_id":"nd_0000000000000002","label":"supports"}`),
	contract.ToolClaim:       []byte(`{"node_id":"nd_0000000000000001"}`),
	contract.ToolSpawn:       []byte(`{"agents":[{"node_id":"nd_0000000000000001","goal":"Find three examples"}]}`),
	contract.ToolAskHuman:    []byte(`{"question":"Which segment first?"}`),
	contract.ToolWriteDoc:    []byte(`{"title":"Pricing","path":"pricing/index.md","body":"# Pricing"}`),
	contract.ToolReport:      []byte(`{"summary":"Added 5 nodes.","status":"review"}`),
}
