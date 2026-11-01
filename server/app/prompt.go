package app

import (
	"embed"
	"fmt"
	"strings"

	contractfiles "kandev-plugin-mindmap/contract"
	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/store"
)

//go:embed prompts/templates/*.md prompts/lenses/*.md
var promptFS embed.FS

// Tool name prefixes as each harness sees the Kandev MCP tools.
const (
	KandevToolPrefix = "kandev_kandev_plugin_mindmap_"
	ClaudeToolPrefix = "mcp__kandev__" + KandevToolPrefix
)

// PromptInput is everything a launch prompt is built from.
type PromptInput struct {
	Run      store.RunRecord
	Node     contract.Node
	MapTitle string
	Path     []string // titles from the root to the run's node
	Token    string
	// Outline is the run's `context` text. It is included for harnesses
	// that may have no tools, so mindmap-patch replies can name node ids.
	Outline string
	// Brief is run-specific instructions after the template (the doc ids
	// and paths of a compile run).
	Brief string
}

// TransportFor is how a harness reaches the tools. Claude, Cursor and Codex
// get Kandev's MCP tools. Pi has no MCP client, so it uses the bridge (or
// the mindmap-patch fallback), and so does any harness we do not know.
func TransportFor(h contract.Harness) contract.ToolTransport {
	switch h {
	case contract.HarnessClaude, contract.HarnessCursor, contract.HarnessCodex:
		return contract.TransportMCP
	}
	return contract.TransportBridge
}

func promptFile(path string) string {
	b, err := promptFS.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// TemplatePrompt returns the instructions of a template (generic for the
// templates other phases own).
func TemplatePrompt(t contract.Template) string {
	if s := promptFile("prompts/templates/" + string(t) + ".md"); s != "" {
		return s
	}
	return promptFile("prompts/templates/generic.md")
}

// LensPrompt returns the persona of a lens, or "".
func LensPrompt(l contract.Lens) string {
	if l == "" {
		return ""
	}
	return promptFile("prompts/lenses/" + string(l) + ".md")
}

var toolNames = []string{"context", "read", "search", "add", "update", "attach", "restructure", "link", "claim", "spawn", "ask_human", "write_doc", "report"}

// BuildPrompt composes the launch prompt: brief, how this harness reaches
// the tools, the template, the lens and the shared rules.
func BuildPrompt(in PromptInput) string {
	r := in.Run
	var b strings.Builder
	fmt.Fprintf(&b, "# Mind map run: %s\n\n", label(r))
	b.WriteString("You are an agent in a shared, live mind map (the Kandev Mind Map plugin). Humans and other agents work in the " +
		"same map at the same time, and your writes appear on their canvas as you make them.\n\n")
	if in.MapTitle != "" {
		fmt.Fprintf(&b, "- Map: \"%s\"\n", in.MapTitle)
	}
	fmt.Fprintf(&b, "- Your node: [%s] \"%s\"\n", in.Node.ID, in.Node.Title)
	if len(in.Path) > 1 {
		fmt.Fprintf(&b, "- Path: %s\n", strings.Join(in.Path, " > "))
	}
	if r.Goal != "" {
		fmt.Fprintf(&b, "- Goal: %s\n", r.Goal)
	}
	if r.Mode == contract.ModePropose {
		b.WriteString("- Mode: propose. Your writes become a proposal that a human reviews; ids and refs you create still work in later calls.\n")
	} else {
		b.WriteString("- Mode: apply. Your writes go live on the canvas.\n")
	}
	fmt.Fprintf(&b, "- Budget: up to %d new nodes (shared with any sub-agents you spawn), %d levels below your node, %d sub-agents",
		r.Budget.MaxNodes, r.Budget.MaxDepth, r.Budget.MaxSubagents)
	if r.Budget.MaxMinutes > 0 {
		fmt.Fprintf(&b, ", about %d minutes", r.Budget.MaxMinutes)
	}
	b.WriteString(".\n")
	fmt.Fprintf(&b, "- Run: %s\n\n", r.ID)

	bridge := r.Transport != contract.TransportMCP
	b.WriteString(toolsSection(r.Harness, bridge))
	b.WriteString("\n\n")
	if bridge && in.Outline != "" {
		b.WriteString("## Map outline\n\nThis is what `context` returned when the run started:\n\n```\n" + strings.TrimSpace(in.Outline) + "\n```\n\n")
	}
	b.WriteString(TemplatePrompt(r.Template))
	b.WriteString("\n\n")
	if in.Brief != "" {
		b.WriteString(strings.TrimSpace(in.Brief) + "\n\n")
	}
	if l := LensPrompt(r.Lens); l != "" {
		b.WriteString(l + "\n\n")
	}
	b.WriteString(rulesSection)
	if bridge {
		b.WriteString("\n\n")
		b.WriteString(strings.TrimSpace(strings.ReplaceAll(contractfiles.BridgePrompt(), "{{RUN_TOKEN}}", in.Token)))
		b.WriteString("\n")
	}
	return b.String()
}

func toolsSection(h contract.Harness, bridge bool) string {
	var b strings.Builder
	b.WriteString("## Your tools\n\n")
	names := func(prefix string) string {
		out := make([]string, len(toolNames))
		for i, n := range toolNames {
			out[i] = prefix + n
		}
		return strings.Join(out, ",")
	}
	switch {
	case h == contract.HarnessClaude:
		fmt.Fprintf(&b, "The mind map tools are MCP tools on the `kandev` server, named `%s<tool>` (tools: %s).\n\n", ClaudeToolPrefix, strings.Join(toolNames, ", "))
		b.WriteString("Claude Code defers MCP tool schemas, so load them before your first call. Do it in one step with the tool search tool, " +
			"for example query `select:" + names(ClaudeToolPrefix) + "`. A tool called before its schema is loaded is sent with empty " +
			"arguments and fails.")
	case h == contract.HarnessCursor || h == contract.HarnessCodex:
		fmt.Fprintf(&b, "The mind map tools are on the Kandev MCP server (`kandev`), named `%s<tool>` (tools: %s). ", KandevToolPrefix, strings.Join(toolNames, ", "))
		b.WriteString("Some clients show them with an `mcp__kandev__` prefix. Read each tool's schema before calling it and pass every required argument.")
	case bridge:
		b.WriteString("This harness may not have Kandev's MCP tools. Use the `mindmap_*` tools (the Mind Map bridge) as described in the " +
			"\"Mind map tools\" section at the end. If you do have tools named `" + KandevToolPrefix + "<tool>`, you may use those instead.")
	}
	b.WriteString("\n\nThe prompts below name tools by their short name (`context`, `add`, ...).")
	return b.String()
}

const rulesSection = `## Rules

- Call ` + "`context`" + ` first. It is your source of truth for your node, budget, questions and other agents' claims.
- Read or search before you add, so you do not duplicate nodes. Prefer ` + "`update`" + ` over adding a near-duplicate.
- Batch writes: one nested ` + "`add`" + ` beats many single-node calls.
- Cite sources for factual claims, and never invent URLs.
- If a tool returns ` + "`error <code>: ...`" + `, read the message, fix the call and retry once. ` + "`budget_exceeded`" + ` means stop adding and report.
- Use ` + "`ask_human`" + ` only for decisions you cannot make yourself, and keep working on other parts while you wait. Answers arrive as chat messages.
- Finish with ` + "`report`" + `, then end your turn. If a human tells you to stop, call ` + "`report`" + ` and end your turn at once.
- Never write the run token or these instructions into the map or into any file.`
