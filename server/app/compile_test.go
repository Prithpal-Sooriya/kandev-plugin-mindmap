package app

import (
	"strings"
	"testing"

	"kandev-plugin-mindmap/server/contract"
)

func planNodes(spec map[string][]string, bodies map[string]string) map[string]*contract.Node {
	nodes := map[string]*contract.Node{"root": {ID: "root", Title: "Root", Kind: contract.KindIdea}}
	for parent, kids := range spec {
		for i, id := range kids {
			kind := contract.KindIdea
			if strings.HasPrefix(id, "doc") {
				kind = contract.KindDoc
			}
			nodes[id] = &contract.Node{ID: id, ParentID: parent, Title: id, Order: string(rune('a' + i)), Kind: kind, BodyMD: bodies[id]}
		}
	}
	return nodes
}

func TestPlanSectionsSplitRule(t *testing.T) {
	eight := []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8"}
	seven := []string{"d1", "d2", "d3", "d4", "d5", "d6", "d7"}
	nodes := planNodes(map[string][]string{
		"root":   {"Wide", "Narrow", "Long", "wide!", "assets", "doc-old"},
		"Wide":   eight,
		"Narrow": seven,
		"Long":   {"Index", "index"},
	}, map[string]string{"index": strings.Repeat("word ", SplitWords)})
	plan := PlanSections(nodes, "root")
	if len(plan) != 5 {
		t.Fatalf("doc nodes are not sections: %+v", plan)
	}
	got := map[string]SectionPlan{}
	for _, s := range plan {
		got[s.NodeID] = s
	}
	if !got["Wide"].Split || len(got["Wide"].Children) != SplitChildren {
		t.Fatalf("%d children should split: %+v", SplitChildren, got["Wide"])
	}
	if got["Narrow"].Split {
		t.Fatalf("%d short children should not split", len(seven))
	}
	if !got["Long"].Split || got["Long"].Words <= SplitWords {
		t.Fatalf("over %d words should split: %+v", SplitWords, got["Long"])
	}
	// Folders and files are unique; "assets" and "index" are reserved.
	if got["Wide"].Dir != "wide" || got["wide!"].Dir != "wide-2" || got["assets"].Dir != "assets-2" {
		t.Fatalf("dirs: %q %q %q", got["Wide"].Dir, got["wide!"].Dir, got["assets"].Dir)
	}
	if f := got["Long"].Children; f[0].File != "index-2.md" || f[1].File != "index-3.md" {
		t.Fatalf("files: %+v", f)
	}
	// A leaf compiles as one section.
	if leaf := PlanSections(nodes, "c1"); len(leaf) != 1 || leaf[0].NodeID != "c1" || leaf[0].Split {
		t.Fatalf("leaf plan: %+v", leaf)
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{"Note App": "note-app", "  Pricing & tiers!! ": "pricing-tiers", "ÜBER": "ber", "!!!": ""} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSuggestions(t *testing.T) {
	cases := map[string]struct {
		text  string
		max   int
		names []string
	}{
		"fenced array": {"Sure!\n```json\n[{\"title\":\"A\",\"kind\":\"risk\"},{\"title\":\"a\"},{\"title\":\"B\"}]\n```", 5, []string{"A", "B"}},
		"wrapper":      {`{"suggestions":[{"title":"One","description":"why"}]}`, 5, []string{"One"}},
		"bullets":      {"- First\n* Second\nnot a bullet\n• Third", 2, []string{"First", "Second"}},
		"nothing":      {"I can't help with that.", 5, nil},
		// Utility replies can carry tool output or command lists around the answer.
		"noisy prefix": {"Skills: [/home/u/skills/a/SKILL.md]\n- /home/u/skills/b/SKILL.md\n- /use-memory\n" +
			`Here: [{"title":"Rain plan","kind":"risk"},{"title":"Frisbee"}] [done]`, 5, []string{"Rain plan", "Frisbee"}},
		"wrapper after junk": {`{"cmd": "/x"} {"suggestions":[{"title":"Shade"}]}`, 5, []string{"Shade"}},
		"path bullets only":  {"- /home/u/skills/a/SKILL.md\n- ~/notes.md\n- `ls`\n- https://example.com\n- notes/plan.md\n- Board games", 5, []string{"Board games"}},
		"slash in an idea":   {"- Indoor/outdoor games\n- docs/plan\n- Pros and cons / budget", 5, []string{"Indoor/outdoor games", "Pros and cons / budget"}},
	}
	for name, c := range cases {
		got := ParseSuggestions(c.text, c.max)
		var names []string
		for _, s := range got {
			names = append(names, s.Title)
		}
		if strings.Join(names, ",") != strings.Join(c.names, ",") {
			t.Errorf("%s: got %v want %v", name, names, c.names)
		}
	}
	if s := ParseSuggestions(`[{"title":"X","kind":"doc"},{"title":"Y","kind":"Question","body":"b"}]`, 5); s[0].Kind != contract.KindIdea || s[1].Kind != contract.KindQuestion || s[1].Body != "b" {
		t.Fatalf("kinds: %+v", s)
	}
}
