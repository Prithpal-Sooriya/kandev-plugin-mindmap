package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func codeOf(err error) ErrorCode {
	if err == nil {
		return ""
	}
	return AsError(err).Code
}

// root
// ├── a
// │   └── b
// │       └── c
// └── d
var testIdx = ParentIndex{
	"nd_root000000": "", "nd_a000000000": "nd_root000000", "nd_b000000000": "nd_a000000000",
	"nd_c000000000": "nd_b000000000", "nd_d000000000": "nd_root000000",
}

const (
	tRoot = "nd_root000000"
	tA    = "nd_a000000000"
	tB    = "nd_b000000000"
	tC    = "nd_c000000000"
	tD    = "nd_d000000000"
)

func TestCheckMove(t *testing.T) {
	cases := []struct {
		id, parent string
		want       ErrorCode
	}{
		{tC, tD, ""},
		{tA, tD, ""},
		{tA, tC, ErrCycle},
		{tA, tA, ErrCycle},
		{tA, tB, ErrCycle},
		{tRoot, tA, ErrRootImmutable},
		{"nd_missing0000", tA, ErrNotFound},
		{tA, "nd_missing0000", ErrNotFound},
	}
	for _, c := range cases {
		if got := codeOf(testIdx.CheckMove(tRoot, c.id, c.parent)); got != c.want {
			t.Errorf("move %s under %s: got %q want %q", c.id, c.parent, got, c.want)
		}
	}
}

func TestTreeHelpers(t *testing.T) {
	if d := testIdx.Depth(tC); d != 3 {
		t.Errorf("depth(c)=%d", d)
	}
	if got := strings.Join(testIdx.Ancestors(tC), ","); got != tRoot+","+tA+","+tB {
		t.Errorf("ancestors(c)=%s", got)
	}
	if !testIdx.IsAncestor(tA, tC) || testIdx.IsAncestor(tC, tA) || testIdx.IsAncestor(tD, tC) {
		t.Error("IsAncestor wrong")
	}
	if codeOf(testIdx.CheckMerge(tRoot, tC, []string{tA})) != ErrCycle {
		t.Error("merging an ancestor of into_id must be a cycle")
	}
	if codeOf(testIdx.CheckMerge(tRoot, tD, []string{tB})) != "" {
		t.Error("merge b into d should be fine")
	}
	if codeOf(testIdx.CheckMerge(tRoot, tD, []string{tRoot})) != ErrRootImmutable {
		t.Error("root cannot be merged away")
	}
	if codeOf(testIdx.CheckDelete(tRoot, tRoot)) != ErrRootImmutable {
		t.Error("root cannot be deleted")
	}
	if codeOf(testIdx.CheckCreate("nd_new00000000", "")) != ErrInvalidOp {
		t.Error("second root must be rejected")
	}
	if codeOf(ParentIndex{}.CheckCreate("nd_new00000000", "")) != "" {
		t.Error("first root is fine")
	}
	if codeOf(testIdx.CheckCreate(tA, tRoot)) != ErrConflict {
		t.Error("duplicate id must conflict")
	}
	cyclic := ParentIndex{"nd_x000000000": "nd_y000000000", "nd_y000000000": "nd_x000000000"}
	if cyclic.Depth("nd_x000000000") != -1 || cyclic.IsAncestor("nd_z000000000", "nd_x000000000") {
		t.Error("cyclic index must terminate")
	}
}

func TestValidateTree(t *testing.T) {
	mk := func(id, parent string) Node { return Node{ID: id, MapID: "map_x000000001", ParentID: parent} }
	ok := []Node{mk(tRoot, ""), mk(tA, tRoot), mk(tB, tA)}
	if err := ValidateTree(ok); err != nil {
		t.Fatal(err)
	}
	if codeOf(ValidateTree([]Node{mk(tRoot, ""), mk(tA, "")})) != ErrInvalidOp {
		t.Error("two roots")
	}
	if codeOf(ValidateTree([]Node{mk(tRoot, ""), mk(tA, tB), mk(tB, tA)})) != ErrCycle {
		t.Error("cycle")
	}
	if codeOf(ValidateTree([]Node{mk(tRoot, ""), mk(tA, "nd_gone00000000")})) != ErrNotFound {
		t.Error("orphan")
	}
	del := mk(tA, tRoot)
	del.Deleted = true
	if codeOf(ValidateTree([]Node{mk(tRoot, ""), del, mk(tB, tA)})) != ErrNotFound {
		t.Error("child of a tombstone must be flagged")
	}
}

func TestOrderBetweenFixtures(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(fixtureDir, "OrderCases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases [][3]string
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		got := OrderBetween(c[0], c[1])
		if got != c[2] {
			t.Errorf("OrderBetween(%q,%q)=%q want %q", c[0], c[1], got, c[2])
		}
	}
	// Repeated inserts at the front, back and middle stay ordered and valid.
	lo, hi := "", ""
	for i := 0; i < 200; i++ {
		k := OrderBetween(lo, hi)
		if !ValidOrderKey(k) || (lo != "" && k <= lo) || (hi != "" && k >= hi) {
			t.Fatalf("step %d: %q not between %q and %q", i, k, lo, hi)
		}
		if i%2 == 0 {
			lo = k
		} else {
			hi = k
		}
	}
	for _, bad := range []string{"", "a0", "a b", strings.Repeat("a", 65)} {
		if ValidOrderKey(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestNextRunStatus(t *testing.T) {
	cases := []struct {
		name   string
		facts  RunFacts
		state  string
		want   RunStatus
		reason WaitingReason
		ok     bool
	}{
		{"starting", RunFacts{}, SessionStarting, RunQueued, "", true},
		{"no downgrade", RunFacts{Status: RunRunning}, SessionCreated, RunRunning, "", false},
		{"running", RunFacts{Status: RunQueued}, SessionRunning, RunRunning, "", true},
		{"follow-up reopens", RunFacts{Status: RunDone}, SessionRunning, RunRunning, "", true},
		{"question", RunFacts{Status: RunRunning, OpenQuestion: true}, SessionWaitingForInput, RunWaiting, WaitingQuestion, true},
		{"reported review", RunFacts{Reported: true, ReportStatus: ReportReview}, SessionWaitingForInput, RunReview, "", true},
		{"reported done", RunFacts{Reported: true, ReportStatus: ReportDone}, SessionWaitingForInput, RunDone, "", true},
		{"reported done but proposal", RunFacts{Reported: true, ReportStatus: ReportDone, OpenProposal: true}, SessionCompleted, RunReview, "", true},
		{"proposal no report", RunFacts{OpenProposal: true}, SessionIdle, RunReview, "", true},
		{"turn ended", RunFacts{Status: RunRunning}, SessionWaitingForInput, RunWaiting, WaitingTurnEnded, true},
		{"completed", RunFacts{Status: RunRunning}, SessionCompleted, RunDone, "", true},
		{"failed", RunFacts{Status: RunRunning}, SessionFailed, RunFailed, "", true},
		{"cancelled", RunFacts{Status: RunRunning}, SessionCancelled, RunStopped, "", true},
		{"stop requested", RunFacts{Status: RunRunning, StopRequested: true}, SessionWaitingForInput, RunStopped, "", true},
		{"missing new_state", RunFacts{Status: RunRunning}, "", RunRunning, "", false},
	}
	for _, c := range cases {
		got, reason, ok := NextRunStatus(c.facts, c.state)
		if got != c.want || reason != c.reason || ok != c.ok {
			t.Errorf("%s: got (%s,%s,%v) want (%s,%s,%v)", c.name, got, reason, ok, c.want, c.reason, c.ok)
		}
	}
	for _, s := range RunStatuses {
		if !NodeStatusForRun(s).Valid() {
			t.Errorf("NodeStatusForRun(%s) invalid", s)
		}
	}
}

func TestExtractPatches(t *testing.T) {
	msg := "I found three examples.\n\n```mindmap-patch\n" +
		`{"v":1,"calls":[{"tool":"add","args":{"nodes":[{"ref":"tiers","title":"Three tiers"}]}},` +
		`{"tool":"link","args":{"from_id":"@tiers","to_id":"nd_question0001","label":"depends on"}}]}` +
		"\n```\n\nAnd a broken one:\n```mindmap-patch\n{\"v\":1,\"calls\":[{\"tool\":\"read\",\"args\":{}}]}\n```\n" +
		"```json\n{\"not\":\"a patch\"}\n```\n```mindmap-patch\n{\"v\":1,"
	patches, errs := ExtractPatches(msg)
	if len(patches) != 1 || len(patches[0].Calls) != 2 {
		t.Fatalf("patches=%+v", patches)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "block 1") || !strings.Contains(errs[0].Error(), "read-only") {
		t.Fatalf("errs=%v", errs)
	}
	for i, raw := range readFixture(t, "Patch") {
		if _, err := ParsePatch(raw); err != nil {
			t.Errorf("Patch[%d]: %v", i, err)
		}
	}
	for _, bad := range []string{
		`{"v":2,"calls":[{"tool":"report","args":{"summary":"s","status":"done"}}]}`,
		`{"v":1,"calls":[]}`,
		`{"v":1,"calls":[{"tool":"attach","args":{"node_id":"nd_x000000001","file_path":"/etc/passwd"}}]}`,
		`{"v":1,"calls":[{"tool":"add","args":{"nodes":[{}]}}]}`,
	} {
		if _, err := ParsePatch([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	// The worked example in docs/protocol.md must parse.
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "protocol.md"))
	if err != nil {
		t.Fatal(err)
	}
	ps, errs := ExtractPatches(string(doc))
	if len(ps) == 0 || len(errs) != 0 {
		t.Errorf("docs/protocol.md patch example: %d patches, errs %v", len(ps), errs)
	}
}

func TestTokens(t *testing.T) {
	secret := strings.Repeat("A", 42) + "_"
	for _, k := range TokenKinds {
		raw := FormatToken(k, 52593, secret)
		p, ok := ParseToken(raw)
		if !ok || p.Kind != k || p.Secret != secret {
			t.Errorf("%s: round trip failed for %q: %+v", k, raw, p)
		}
		if (k == TokenRun || k == TokenPersonal) != (p.Port == 52593) {
			t.Errorf("%s: port %d", k, p.Port)
		}
	}
	for _, bad := range []string{"", "mmr1." + secret, "mml1.1.2", "mmx1." + secret, "mmr1.0." + secret, "mms1.short"} {
		if _, ok := ParseToken(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestErrorCodesMapToHTTP(t *testing.T) {
	for _, c := range ErrorCodes {
		s := c.HTTPStatus()
		if s == 500 && c != ErrInternal {
			t.Errorf("%s maps to 500", c)
		}
	}
	res := ToolError(FieldErr(ErrBadRequest, "nodes", "required"))
	if !res.IsError || res.Text != "error bad_request: nodes: required" {
		t.Errorf("ToolError text %q", res.Text)
	}
}

func TestIDsAndRefs(t *testing.T) {
	if !ValidID(PrefixNode, "nd_root00000001") || ValidID(PrefixNode, "map_root0000001") || ValidID(PrefixNode, "nd_short") {
		t.Error("ValidID")
	}
	if !ValidNodeRef("@tiers") || !ValidNodeRef("nd_root00000001") || ValidNodeRef("@Tiers") || ValidNodeRef("tiers") {
		t.Error("ValidNodeRef")
	}
}

func TestEnumsAreValid(t *testing.T) {
	check := func(name string, n int, valid func(int) bool) {
		for i := 0; i < n; i++ {
			if !valid(i) {
				t.Errorf("%s[%d] not Valid()", name, i)
			}
		}
	}
	check("NodeKinds", len(NodeKinds), func(i int) bool { return NodeKinds[i].Valid() })
	check("Templates", len(Templates), func(i int) bool { return Templates[i].Valid() })
	check("ActivityTypes", len(ActivityTypes), func(i int) bool { return ActivityTypes[i].Valid() })
	if TemplateGarden.DefaultMode() != ModePropose || TemplateResearch.DefaultMode() != ModeApply {
		t.Error("garden defaults to propose")
	}
	if k, ok := AssetKindForMIME("image/png"); !ok || k != AssetImage {
		t.Error("png")
	}
	if _, ok := AssetKindForMIME("application/x-sh"); ok {
		t.Error("sh allowed")
	}
}
