package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var toolDir = filepath.Join("..", "..", "contract", "tools")

func loadToolDecls(t *testing.T) []ToolDeclaration {
	t.Helper()
	var out []ToolDeclaration
	for _, name := range ToolNames {
		b, err := os.ReadFile(filepath.Join(toolDir, string(name)+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var d ToolDeclaration
		if err := StrictUnmarshal(b, &d); err != nil {
			t.Fatalf("%s.json: %v", name, err)
		}
		if d.Name != string(name) {
			t.Errorf("%s.json: name %q", name, d.Name)
		}
		out = append(out, d)
	}
	entries, _ := os.ReadDir(toolDir)
	if len(entries) != len(ToolNames) {
		t.Errorf("contract/tools has %d files, want %d", len(entries), len(ToolNames))
	}
	return out
}

// TestToolDeclarationsFitKandevLimits mirrors Kandev's manifest validation
// (internal/plugins/manifest/validate.go and internal/mcp/toolschema).
func TestToolDeclarationsFitKandevLimits(t *testing.T) {
	decls := loadToolDecls(t)
	if len(decls) > 16 {
		t.Fatalf("%d tools; Kandev allows 16", len(decls))
	}
	total := 0
	for _, d := range decls {
		if l := len(d.Description); l == 0 || l > 1024 {
			t.Errorf("%s: description is %d bytes; Kandev allows 1-1024", d.Name, l)
		}
		if len(d.Name) > 32 || strings.ToLower(d.Name) != d.Name {
			t.Errorf("%s: bad name", d.Name)
		}
		b, _ := json.Marshal(d.InputSchema)
		total += len(b)
		if len(b) > 64*1024 {
			t.Errorf("%s: schema is %d bytes", d.Name, len(b))
		}
		if d.InputSchema["type"] != "object" {
			t.Errorf("%s: root type must be object", d.Name)
		}
		for _, k := range []string{"oneOf", "anyOf", "allOf", "$ref", "definitions", "$defs"} {
			if _, ok := d.InputSchema[k]; ok {
				t.Errorf("%s: root must not use %s", d.Name, k)
			}
		}
		walkSchema(t, d.Name, d.InputSchema)
		if d.Annotations.ReadOnlyHint != NewToolName(d.Name).ReadOnly() {
			t.Errorf("%s: read_only_hint disagrees with ToolName.ReadOnly", d.Name)
		}
		if d.Annotations.ReadOnlyHint && d.Annotations.DestructiveHint {
			t.Errorf("%s: read_only and destructive", d.Name)
		}
	}
	if total > 64*1024 {
		t.Errorf("total schema size %d bytes exceeds 64 KiB", total)
	}
	t.Logf("total compact input_schema size: %d bytes", total)
}

func NewToolName(s string) ToolName { return ToolName(s) }

// limitsByProperty pins schema limits to the Go constants.
var limitsByProperty = map[string]int{
	"title": MaxTitleLen, "body": MaxBodyLen, "append_body": MaxBodyLen, "quote": MaxQuoteLen,
	"url": MaxURLLen, "source_url": MaxURLLen, "label": MaxLinkLabelLen, "reason": MaxLinkReasonLen,
	"caption": MaxCaptionLen, "question": MaxQuestionLen, "goal": MaxQuestionLen, "summary": MaxSummaryLen,
	"base64": MaxBase64Chars, "path": MaxDocPathLen, "note": MaxCaptionLen, "context": MaxBodyLen,
}

var itemLimitsByProperty = map[string]int{
	"tags": MaxTags, "add_tags": MaxTags, "sources": MaxSources, "add_sources": MaxSources,
	"ops": MaxRestructureOps, "agents": MaxSpawnPerCall,
}

// walkSchema checks every object forbids extra properties, every property has
// a description (except enum-only op discriminators), and limits match Go.
func walkSchema(t *testing.T, path string, s map[string]any) {
	t.Helper()
	switch s["type"] {
	case "object":
		if s["additionalProperties"] != false {
			t.Errorf("%s: object must set additionalProperties: false", path)
		}
		props, _ := s["properties"].(map[string]any)
		for name, p := range props {
			ps := p.(map[string]any)
			if _, ok := ps["description"]; !ok && name != "op" && !strings.Contains(path, ".budget") && !strings.HasSuffix(path, ".children") {
				t.Errorf("%s.%s: missing description", path, name)
			}
			want, ok := limitsByProperty[name]
			if name == "title" && strings.HasSuffix(path, "sources[]") {
				want = MaxSourceTitleLen
			}
			if ok && ps["type"] == "string" {
				if got, _ := ps["maxLength"].(float64); int(got) != want {
					t.Errorf("%s.%s: maxLength %v, Go limit %d", path, name, ps["maxLength"], want)
				}
			}
			if want, ok := itemLimitsByProperty[name]; ok {
				if got, _ := ps["maxItems"].(float64); int(got) != want {
					t.Errorf("%s.%s: maxItems %v, Go limit %d", path, name, ps["maxItems"], want)
				}
			}
			walkSchema(t, path+"."+name, ps)
		}
	case "array":
		if _, ok := s["maxItems"]; !ok {
			t.Errorf("%s: array without maxItems", path)
		}
		if items, ok := s["items"].(map[string]any); ok {
			walkSchema(t, path+"[]", items)
		}
	case "string":
		if _, ok := s["maxLength"]; !ok {
			if _, isEnum := s["enum"]; !isEnum && s["format"] == nil {
				t.Errorf("%s: string without maxLength", path)
			}
		}
	}
}

// TestToolSchemasMatchGoArgs checks the JSON Schema of each tool against its
// Go args struct, recursively: same property names, same required set.
func TestToolSchemasMatchGoArgs(t *testing.T) {
	for _, d := range loadToolDecls(t) {
		typ := reflect.TypeOf(NewToolArgs(ToolName(d.Name))).Elem()
		compareSchema(t, d.Name, d.InputSchema, typ)
	}
}

func compareSchema(t *testing.T, path string, s map[string]any, typ reflect.Type) {
	t.Helper()
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return
	}
	props, _ := s["properties"].(map[string]any)
	var required []string
	if r, ok := s["required"].([]any); ok {
		for _, x := range r {
			required = append(required, x.(string))
		}
	}
	goFields := map[string]reflect.StructField{}
	var goRequired []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		parts := strings.Split(f.Tag.Get("json"), ",")
		goFields[parts[0]] = f
		if !slices.Contains(parts[1:], "omitempty") && !slices.Contains(parts[1:], "omitzero") {
			goRequired = append(goRequired, parts[0])
		}
	}
	for name := range props {
		if _, ok := goFields[name]; !ok {
			t.Errorf("%s: schema property %q has no Go field in %s", path, name, typ.Name())
		}
	}
	for name, f := range goFields {
		p, ok := props[name]
		if !ok {
			// The add schema unrolls children 3 levels; the deepest level has none.
			if !(name == "children" && strings.Count(path, ".children") == MaxAddDepth-1) {
				t.Errorf("%s: Go field %s.%s missing from schema", path, typ.Name(), name)
			}
			continue
		}
		ps := p.(map[string]any)
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch ft.Kind() {
		case reflect.Struct:
			if ft.PkgPath() == typ.PkgPath() {
				compareSchema(t, path+"."+name, ps, ft)
			}
		case reflect.Slice:
			et := ft.Elem()
			if et.Kind() == reflect.Struct && et.PkgPath() == typ.PkgPath() {
				compareSchema(t, path+"."+name, ps["items"].(map[string]any), et)
			}
		}
	}
	sort.Strings(required)
	sort.Strings(goRequired)
	if !slices.Equal(required, goRequired) {
		t.Errorf("%s: schema required %v, Go required (no omitempty) %v", path, required, goRequired)
	}
}

type toolArgCases struct {
	Valid    []json.RawMessage `json:"valid"`
	Invalid  []json.RawMessage `json:"invalid"`
	Semantic []json.RawMessage `json:"semantic"`
}

// TestToolArgFixtures: valid cases decode and validate; invalid (schema-level)
// and semantic (cross-field) cases are rejected with bad_request-class errors.
// vitest checks the same files against the JSON schemas.
func TestToolArgFixtures(t *testing.T) {
	for _, name := range ToolNames {
		b, err := os.ReadFile(filepath.Join(fixtureDir, "tool-args", string(name)+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var c toolArgCases
		if err := StrictUnmarshal(b, &c); err != nil {
			t.Fatal(err)
		}
		if len(c.Valid) == 0 || len(c.Invalid) == 0 {
			t.Errorf("%s: need valid and invalid cases", name)
		}
		for i, raw := range c.Valid {
			if _, err := DecodeToolArgs(name, raw); err != nil {
				t.Errorf("%s valid[%d]: %v", name, i, err)
			}
		}
		for i, raw := range append(append([]json.RawMessage{}, c.Invalid...), c.Semantic...) {
			_, err := DecodeToolArgs(name, raw)
			if err == nil {
				t.Errorf("%s invalid/semantic[%d] %s: accepted", name, i, raw)
				continue
			}
			switch AsError(err).Code {
			case ErrBadRequest, ErrTooLarge, ErrUnsupportedMedia:
			default:
				t.Errorf("%s case %d: unexpected code %s", name, i, AsError(err).Code)
			}
		}
	}
}

type manifestDoc struct {
	AgentTools []struct {
		Name        string         `yaml:"name"`
		Description string         `yaml:"description"`
		Surfaces    []string       `yaml:"surfaces"`
		Annotations map[string]any `yaml:"annotations"`
		InputSchema map[string]any `yaml:"input_schema"`
	} `yaml:"agent_tools"`
}

func normalizeJSON(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// TestManifestAgentToolsMatchContract parses manifest.yaml the way Kandev does
// (yaml.v3) and checks agent_tools equals contract/tools exactly, in order.
func TestManifestAgentToolsMatchContract(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var m manifestDoc
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	decls := loadToolDecls(t)
	if len(m.AgentTools) != len(decls) {
		t.Fatalf("manifest declares %d tools, contract has %d (run npm run sync:tools)", len(m.AgentTools), len(decls))
	}
	for i, d := range decls {
		got := m.AgentTools[i]
		if got.Name != d.Name || got.Description != d.Description {
			t.Errorf("tool %d: manifest %q differs from contract %q (run npm run sync:tools)", i, got.Name, d.Name)
		}
		if !slices.Equal(got.Surfaces, []string{"kanban-task", "office-task"}) {
			t.Errorf("%s: surfaces %v", d.Name, got.Surfaces)
		}
		if !reflect.DeepEqual(normalizeJSON(t, got.InputSchema), normalizeJSON(t, d.InputSchema)) {
			t.Errorf("%s: manifest input_schema differs from contract/tools (run npm run sync:tools)", d.Name)
		}
		if !reflect.DeepEqual(normalizeJSON(t, got.Annotations), normalizeJSON(t, d.Annotations)) {
			t.Errorf("%s: manifest annotations differ", d.Name)
		}
	}
}
