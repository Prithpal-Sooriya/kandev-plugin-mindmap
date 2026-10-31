package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// fixtureDir is contract/fixtures, shared with the vitest suite.
var fixtureDir = filepath.Join("..", "..", "contract", "fixtures")

// fixtureTypes maps each contract/fixtures/<Name>.json to the Go type every
// element of its array must decode into. ui/src/contract/shapes.ts has the
// same table; both suites fail if a fixture file is missing or extra.
var fixtureTypes = map[string]func() any{
	"Map":                   func() any { return &Map{} },
	"Node":                  func() any { return &Node{} },
	"Link":                  func() any { return &Link{} },
	"Run":                   func() any { return &Run{} },
	"Op":                    func() any { return &Op{} },
	"Proposal":              func() any { return &Proposal{} },
	"Participant":           func() any { return &Participant{} },
	"Presence":              func() any { return &Presence{} },
	"ActivityEvent":         func() any { return &ActivityEvent{} },
	"DocTree":               func() any { return &DocTree{} },
	"ClientMessage":         func() any { return &Message{} },
	"ServerMessage":         func() any { return &Message{} },
	"Error":                 func() any { return &Error{} },
	"ErrorBody":             func() any { return &ErrorBody{} },
	"ToolResult":            func() any { return &ToolResult{} },
	"WriteOutcome":          func() any { return &WriteOutcome{} },
	"SessionResponse":       func() any { return &SessionResponse{} },
	"SyncResponse":          func() any { return &SyncResponse{} },
	"SubmitRequest":         func() any { return &SubmitRequest{} },
	"MapSummary":            func() any { return &MapSummary{} },
	"CreateMapRequest":      func() any { return &CreateMapRequest{} },
	"StartRunRequest":       func() any { return &StartRunRequest{} },
	"SwarmRequest":          func() any { return &SwarmRequest{} },
	"CompileRequest":        func() any { return &CompileRequest{} },
	"StartRunsResponse":     func() any { return &StartRunsResponse{} },
	"SuggestRequest":        func() any { return &SuggestRequest{} },
	"SuggestResponse":       func() any { return &SuggestResponse{} },
	"ReviewProposalRequest": func() any { return &ReviewProposalRequest{} },
	"MessageRunRequest":     func() any { return &MessageRunRequest{} },
	"AgentProfile":          func() any { return &AgentProfile{} },
	"ShareLink":             func() any { return &ShareLink{} },
	"ShareSettings":         func() any { return &ShareSettings{} },
	"Patch":                 func() any { return &Patch{} },
}

// nonShapeFixtures are fixture files that are test vectors, not wire shapes.
var nonShapeFixtures = map[string]bool{"OrderCases": true}

func readFixture(t *testing.T, name string) []json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(b, &items); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(items) == 0 {
		t.Fatalf("%s: empty fixture", name)
	}
	return items
}

func generic(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestFixtureFilesMatchRegistry(t *testing.T) {
	entries, err := os.ReadDir(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		seen[name] = true
		if fixtureTypes[name] == nil && !nonShapeFixtures[name] {
			t.Errorf("fixture %s.json has no Go type in fixtureTypes", name)
		}
	}
	for name := range fixtureTypes {
		if !seen[name] {
			t.Errorf("fixtureTypes[%s] has no contract/fixtures/%s.json", name, name)
		}
	}
}

// TestFixturesRoundTrip decodes every fixture strictly into its Go type and
// re-encodes it. The output must equal the input exactly: no unknown fields,
// no dropped fields, and the same omit-when-empty behaviour as the TS mirror.
// It also records which struct fields were ever non-zero, and fails if any
// contract struct field is never exercised, so fixtures cannot silently drift
// behind the types.
func TestFixturesRoundTrip(t *testing.T) {
	cov := newCoverage()
	for name, mk := range fixtureTypes {
		for i, raw := range readFixture(t, name) {
			v := mk()
			if err := StrictUnmarshal(raw, v); err != nil {
				t.Errorf("%s[%d]: strict decode: %v", name, i, err)
				continue
			}
			out, err := json.Marshal(v)
			if err != nil {
				t.Errorf("%s[%d]: encode: %v", name, i, err)
				continue
			}
			if !reflect.DeepEqual(generic(t, raw), generic(t, out)) {
				t.Errorf("%s[%d]: round trip changed the JSON\n in: %s\nout: %s", name, i, compact(raw), out)
			}
			cov.walk(reflect.ValueOf(v))
		}
	}
	for _, miss := range cov.missing() {
		t.Errorf("no fixture sets %s", miss)
	}
}

func compact(b []byte) string {
	var buf bytes.Buffer
	_ = json.Compact(&buf, b)
	return buf.String()
}

func TestFixtureOpsAndMessagesValidate(t *testing.T) {
	for i, raw := range readFixture(t, "Op") {
		var op Op
		if err := json.Unmarshal(raw, &op); err != nil {
			t.Fatal(err)
		}
		if err := op.Validate(); err != nil {
			t.Errorf("Op[%d] %s: %v", i, op.Type, err)
		}
	}
	types := map[MsgType]bool{}
	for _, f := range []string{"ClientMessage", "ServerMessage"} {
		for i, raw := range readFixture(t, f) {
			var m Message
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("%s[%d]: %v", f, i, err)
			}
			types[m.Type] = true
			if s, ok := m.Payload.(*SubmitPayload); ok {
				for _, op := range s.Ops {
					if err := op.Validate(); err != nil {
						t.Errorf("%s[%d] submit op: %v", f, i, err)
					}
				}
			}
			if s, ok := m.Payload.(*Snapshot); ok {
				if err := ValidateTree(s.Nodes); err != nil {
					t.Errorf("%s[%d] snapshot tree: %v", f, i, err)
				}
			}
		}
	}
	for _, mt := range append(append([]MsgType{}, ClientMsgTypes...), ServerMsgTypes...) {
		if !types[mt] {
			t.Errorf("no fixture for message type %s", mt)
		}
	}
	opTypes := map[OpType]bool{}
	for _, raw := range readFixture(t, "Op") {
		var op Op
		_ = json.Unmarshal(raw, &op)
		opTypes[op.Type] = true
	}
	for _, ot := range OpTypes {
		if !opTypes[ot] {
			t.Errorf("no Op fixture for %s", ot)
		}
	}
}

func TestOpRejectsUnknownTypeAndFields(t *testing.T) {
	for _, bad := range []string{
		`{"id":"op_x000000001","map_id":"map_x000000001","rev":0,"type":"node.explode","payload":{},"actor":{"participant_id":"pt_x000000001"}}`,
		`{"id":"op_x000000001","map_id":"map_x000000001","rev":0,"type":"node.delete","payload":{"id":"nd_x000000001","extra":1},"actor":{"participant_id":"pt_x000000001"}}`,
		`{"id":"op_x000000001","map_id":"map_x000000001","rev":0,"type":"node.delete","actor":{"participant_id":"pt_x000000001"}}`,
	} {
		var op Op
		if err := json.Unmarshal([]byte(bad), &op); err == nil {
			t.Errorf("expected error for %s", bad)
		}
	}
	if _, err := json.Marshal(Op{ID: "op_x", Type: OpNodeMove, Payload: &NodeDeletePayload{}}); err == nil {
		t.Error("expected type/payload mismatch error")
	}
}

// --- coverage -------------------------------------------------------------------

type coverage struct {
	fields map[string]map[string]bool // type -> json field -> seen non-zero
}

func newCoverage() *coverage { return &coverage{fields: map[string]map[string]bool{}} }

func (c *coverage) walk(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			c.walk(v.Elem())
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			c.walk(v.Index(i))
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			c.walk(v.MapIndex(k))
		}
	case reflect.Struct:
		t := v.Type()
		if t.PkgPath() != reflect.TypeFor[Map]().PkgPath() {
			return // time.Time etc.
		}
		name := t.Name()
		if c.fields[name] == nil {
			c.fields[name] = map[string]bool{}
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")[0]
			if !f.IsExported() || tag == "-" {
				continue
			}
			if tag == "" {
				tag = f.Name
			}
			fv := v.Field(i)
			if !fv.IsZero() {
				c.fields[name][tag] = true
			} else if _, ok := c.fields[name][tag]; !ok {
				c.fields[name][tag] = false
			}
			c.walk(fv)
		}
	}
}

func (c *coverage) missing() []string {
	var out []string
	for typ, fs := range c.fields {
		for f, ok := range fs {
			if !ok {
				out = append(out, typ+"."+f)
			}
		}
	}
	sort.Strings(out)
	return out
}
