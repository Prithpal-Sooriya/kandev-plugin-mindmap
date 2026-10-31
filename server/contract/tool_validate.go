package contract

import (
	"encoding/json"
	"fmt"
	"slices"
)

// Tool argument validation beyond what JSON Schema expresses (cross-field
// rules, refs, totals). Kandev already enforces the schema on the MCP path,
// but the bridge and the prompt fallback do not, so the backend always runs
// DecodeToolArgs.

// ToolArgsValidator is implemented by every *Args struct.
type ToolArgsValidator interface{ Validate() error }

// DecodeToolArgs strictly decodes raw JSON arguments for tool t and validates
// them. Missing required arguments produce bad_request errors that name the
// field, which is what agents with deferred schemas need to self-correct.
func DecodeToolArgs(t ToolName, raw []byte) (any, error) {
	args := NewToolArgs(t)
	if args == nil {
		return nil, Errf(ErrNotFound, "unknown tool %q", t)
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	if err := StrictUnmarshal(raw, args); err != nil {
		return nil, Errf(ErrBadRequest, "invalid arguments for %s: %v", t, err)
	}
	if v, ok := args.(ToolArgsValidator); ok {
		if err := v.Validate(); err != nil {
			// Shared field validators speak in op terms; tools report bad_request.
			e := *AsError(err)
			if e.Code == ErrInvalidOp {
				e.Code = ErrBadRequest
			}
			return nil, &e
		}
	}
	return args, nil
}

// DecodeToolArgsMap is DecodeToolArgs for pluginsdk's map arguments.
func DecodeToolArgsMap(t ToolName, m map[string]any) (any, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, Errf(ErrBadRequest, "arguments are not JSON: %v", err)
	}
	return DecodeToolArgs(t, raw)
}

func argErr(field, format string, a ...any) error {
	return FieldErr(ErrBadRequest, field, format, a...)
}

func requireNodeRef(field, s string) error {
	if s == "" {
		return argErr(field, "required: a node id like nd_... or an @ref")
	}
	return optionalNodeRef(field, s)
}

func optionalNodeRef(field, s string) error {
	if s != "" && !ValidNodeRef(s) {
		return argErr(field, "expected a node id like nd_... or an @ref, got %q", s)
	}
	return nil
}

func optionalRef(field, s string) error {
	if s != "" && !ValidRef(s) {
		return argErr(field, "ref must match [a-z0-9][a-z0-9_-]{0,47} (no @)")
	}
	return nil
}

func (a *ContextArgs) Validate() error {
	if a.SubtreeDepth < 0 || a.SubtreeDepth > 6 {
		return argErr("subtree_depth", "must be 0-6")
	}
	return nil
}

func (a *ReadArgs) Validate() error {
	if a.MapID != "" && !ValidID(PrefixMap, a.MapID) {
		return argErr("map_id", "expected a map id like map_...")
	}
	if a.Depth < 0 || a.Depth > 10 {
		return argErr("depth", "must be 1-10")
	}
	if a.Limit < 0 || a.Limit > MaxReadLimit {
		return argErr("limit", "must be 1-%d", MaxReadLimit)
	}
	return optionalNodeRef("node_id", a.NodeID)
}

func (a *SearchArgs) Validate() error {
	if a.Query == "" && len(a.Kinds) == 0 && len(a.Tags) == 0 && len(a.Statuses) == 0 {
		return argErr("query", "give a query or at least one filter")
	}
	for i, k := range a.Kinds {
		if !k.Valid() {
			return argErr(fmt.Sprintf("kinds[%d]", i), "unknown kind %q", k)
		}
	}
	for i, s := range a.Statuses {
		if !s.Valid() {
			return argErr(fmt.Sprintf("statuses[%d]", i), "unknown status %q", s)
		}
	}
	switch a.Scope {
	case "", ScopeMap, ScopeWorkspace:
	case ScopeSubtree:
		if a.NodeID == "" {
			return argErr("node_id", "required when scope is subtree")
		}
	default:
		return argErr("scope", "must be map, subtree or workspace")
	}
	if a.Limit < 0 || a.Limit > MaxSearchLimit {
		return argErr("limit", "must be 1-%d", MaxSearchLimit)
	}
	return optionalNodeRef("node_id", a.NodeID)
}

func validateAddNode(field string, n *AddNode, depth int, count *int, refs map[string]bool) error {
	*count++
	if *count > MaxAddNodes {
		return FieldErr(ErrTooLarge, field, "at most %d nodes per add call; split the batch", MaxAddNodes)
	}
	if depth > MaxAddDepth {
		return FieldErr(ErrTooLarge, field, "children nest at most %d levels per call", MaxAddDepth)
	}
	if err := ValidateTitle(field+".title", n.Title); err != nil {
		return argErr(field+".title", "%s", AsError(err).Message)
	}
	if n.Kind != "" && (!n.Kind.Valid() || n.Kind == KindDoc) {
		return argErr(field+".kind", "unknown kind %q (use write_doc for docs)", n.Kind)
	}
	if err := optionalRef(field+".ref", n.Ref); err != nil {
		return err
	}
	if n.Ref != "" {
		if refs[n.Ref] {
			return argErr(field+".ref", "ref %q used twice in this call", n.Ref)
		}
		refs[n.Ref] = true
	}
	if err := checkLen(field+".body", n.Body, MaxBodyLen, false); err != nil {
		return err
	}
	if err := ValidateTags(field+".tags", n.Tags); err != nil {
		return err
	}
	if err := ValidateConfidence(field+".confidence", n.Confidence); err != nil {
		return err
	}
	if err := ValidateSources(field+".sources", n.Sources); err != nil {
		return err
	}
	for i := range n.Children {
		if err := validateAddNode(fmt.Sprintf("%s.children[%d]", field, i), &n.Children[i], depth+1, count, refs); err != nil {
			return err
		}
	}
	return nil
}

// CountAddNodes returns the total number of nodes in an add call.
func (a *AddArgs) CountNodes() int {
	var walk func([]AddNode) int
	walk = func(ns []AddNode) int {
		c := 0
		for _, n := range ns {
			c += 1 + walk(n.Children)
		}
		return c
	}
	return walk(a.Nodes)
}

func (a *AddArgs) Validate() error {
	if len(a.Nodes) == 0 {
		return argErr("nodes", "required: at least one node")
	}
	if err := optionalNodeRef("parent_id", a.ParentID); err != nil {
		return err
	}
	if err := optionalNodeRef("after_id", a.AfterID); err != nil {
		return err
	}
	count, refs := 0, map[string]bool{}
	for i := range a.Nodes {
		if err := validateAddNode(fmt.Sprintf("nodes[%d]", i), &a.Nodes[i], 1, &count, refs); err != nil {
			return err
		}
	}
	return nil
}

func (a *UpdateArgs) Validate() error {
	if err := requireNodeRef("node_id", a.NodeID); err != nil {
		return err
	}
	if a.Title == "" && a.Body == nil && a.AppendBody == "" && a.Kind == "" && len(a.AddTags) == 0 &&
		len(a.RemoveTags) == 0 && a.Confidence == nil && len(a.AddSources) == 0 {
		return argErr("node_id", "nothing to update: set at least one other field")
	}
	if a.Body != nil && a.AppendBody != "" {
		return argErr("append_body", "use body (replace) or append_body, not both")
	}
	if a.Title != "" {
		if err := ValidateTitle("title", a.Title); err != nil {
			return err
		}
	}
	if a.Kind != "" && (!a.Kind.Valid() || a.Kind == KindDoc) {
		return argErr("kind", "unknown kind %q", a.Kind)
	}
	if err := ValidateTags("add_tags", a.AddTags); err != nil {
		return err
	}
	if err := ValidateConfidence("confidence", a.Confidence); err != nil {
		return err
	}
	return ValidateSources("add_sources", a.AddSources)
}

func (a *AttachArgs) Validate() error {
	if err := requireNodeRef("node_id", a.NodeID); err != nil {
		return err
	}
	n := 0
	for _, s := range []string{a.URL, a.FilePath, a.Base64} {
		if s != "" {
			n++
		}
	}
	if n != 1 {
		return argErr("url", "give exactly one of url, file_path or base64")
	}
	if a.URL != "" {
		if err := ValidateHTTPURL("url", a.URL); err != nil {
			return err
		}
	}
	if a.Base64 != "" {
		if a.MIMEType == "" {
			return argErr("mime_type", "required with base64")
		}
		if len(a.Base64) > MaxBase64Chars {
			return FieldErr(ErrTooLarge, "base64", "base64 over %d chars; save to a file and use file_path", MaxBase64Chars)
		}
	}
	if a.MIMEType != "" {
		if _, ok := AssetKindForMIME(a.MIMEType); !ok {
			return FieldErr(ErrUnsupportedMedia, "mime_type", "MIME type %q not allowed", a.MIMEType)
		}
	}
	if a.SourceURL != "" {
		if err := ValidateHTTPURL("source_url", a.SourceURL); err != nil {
			return err
		}
	}
	return checkLen("caption", a.Caption, MaxCaptionLen, false)
}

func (a *RestructureArgs) Validate() error {
	if err := checkLen("reason", a.Reason, MaxLinkReasonLen, true); err != nil {
		return argErr("reason", "required: one sentence on why (shown to reviewers)")
	}
	if len(a.Ops) == 0 || len(a.Ops) > MaxRestructureOps {
		return argErr("ops", "give 1-%d ops", MaxRestructureOps)
	}
	for i, op := range a.Ops {
		f := fmt.Sprintf("ops[%d]", i)
		if err := op.validate(f); err != nil {
			return err
		}
	}
	return nil
}

func (op *RestructureOp) validate(f string) error {
	if !slices.Contains(RestructureKinds, op.Op) {
		return argErr(f+".op", "must be move, merge, regroup, rename or retire")
	}
	if err := optionalRef(f+".ref", op.Ref); err != nil {
		return err
	}
	switch op.Op {
	case RestructureMove:
		if err := requireNodeRef(f+".node_id", op.NodeID); err != nil {
			return err
		}
		if err := requireNodeRef(f+".parent_id", op.ParentID); err != nil {
			return err
		}
		return optionalNodeRef(f+".after_id", op.AfterID)
	case RestructureMerge:
		if err := requireNodeRef(f+".into_id", op.IntoID); err != nil {
			return err
		}
		if len(op.NodeIDs) == 0 || len(op.NodeIDs) > MaxMergeSources {
			return argErr(f+".node_ids", "merge needs 1-%d node_ids", MaxMergeSources)
		}
	case RestructureRegroup:
		if len(op.NodeIDs) == 0 {
			return argErr(f+".node_ids", "regroup needs the node_ids to move under the new parent")
		}
		if err := ValidateTitle(f+".title", op.Title); err != nil {
			return argErr(f+".title", "regroup needs a title for the new parent")
		}
		if err := optionalNodeRef(f+".parent_id", op.ParentID); err != nil {
			return err
		}
	case RestructureRename:
		if err := requireNodeRef(f+".node_id", op.NodeID); err != nil {
			return err
		}
		if err := ValidateTitle(f+".title", op.Title); err != nil {
			return argErr(f+".title", "rename needs the new title")
		}
	case RestructureRetire:
		if err := requireNodeRef(f+".node_id", op.NodeID); err != nil {
			return err
		}
	}
	for j, id := range op.NodeIDs {
		if err := requireNodeRef(fmt.Sprintf("%s.node_ids[%d]", f, j), id); err != nil {
			return err
		}
	}
	return nil
}

func (a *LinkArgs) Validate() error {
	if err := requireNodeRef("from_id", a.FromID); err != nil {
		return err
	}
	if err := requireNodeRef("to_id", a.ToID); err != nil {
		return err
	}
	if a.FromID == a.ToID {
		return argErr("to_id", "a link must join two different nodes")
	}
	if err := checkLen("label", a.Label, MaxLinkLabelLen, true); err != nil {
		return err
	}
	return checkLen("reason", a.Reason, MaxLinkReasonLen, false)
}

func (a *ClaimArgs) Validate() error {
	if err := requireNodeRef("node_id", a.NodeID); err != nil {
		return err
	}
	if a.Action != "" && a.Action != ClaimActionClaim && a.Action != ClaimActionRelease {
		return argErr("action", "must be claim or release")
	}
	if a.TTLMinutes < 0 || a.TTLMinutes > int(MaxClaimTTL.Minutes()) {
		return argErr("ttl_minutes", "must be 1-%d", int(MaxClaimTTL.Minutes()))
	}
	return checkLen("note", a.Note, MaxCaptionLen, false)
}

func (a *SpawnArgs) Validate() error {
	if len(a.Agents) == 0 || len(a.Agents) > MaxSpawnPerCall {
		return argErr("agents", "give 1-%d agents", MaxSpawnPerCall)
	}
	for i, ag := range a.Agents {
		f := fmt.Sprintf("agents[%d]", i)
		if err := requireNodeRef(f+".node_id", ag.NodeID); err != nil {
			return err
		}
		if err := checkLen(f+".goal", ag.Goal, MaxQuestionLen, true); err != nil {
			return argErr(f+".goal", "required: what the sub-agent should achieve")
		}
		if ag.Template != "" && !slices.Contains([]Template{TemplateResearch, TemplateExplore, TemplateSwarm, TemplateCritique}, ag.Template) {
			return argErr(f+".template", "must be research, explore, swarm or critique")
		}
		if ag.Lens != "" && !ag.Lens.Valid() {
			return argErr(f+".lens", "unknown lens %q", ag.Lens)
		}
		if ag.Mode != "" && !ag.Mode.Valid() {
			return argErr(f+".mode", "must be apply or propose")
		}
		if err := ag.Budget.Validate(f + ".budget"); err != nil {
			return err
		}
	}
	return nil
}

func (a *AskHumanArgs) Validate() error {
	if err := checkLen("question", a.Question, MaxQuestionLen, true); err != nil {
		return err
	}
	if len(a.Options) > 6 {
		return argErr("options", "at most 6 options")
	}
	if err := checkLen("context", a.Context, MaxBodyLen, false); err != nil {
		return err
	}
	return optionalNodeRef("node_id", a.NodeID)
}

func (a *WriteDocArgs) Validate() error {
	if err := optionalNodeRef("doc_id", a.DocID); err != nil {
		return err
	}
	if err := optionalNodeRef("parent_doc_id", a.ParentDocID); err != nil {
		return err
	}
	if err := ValidateTitle("title", a.Title); err != nil {
		return err
	}
	if err := ValidateDocPath("path", a.Path); err != nil {
		return err
	}
	if err := checkLen("body", a.Body, MaxBodyLen, true); err != nil {
		return err
	}
	for i, id := range a.SourceIDs {
		if err := requireNodeRef(fmt.Sprintf("source_ids[%d]", i), id); err != nil {
			return err
		}
	}
	return optionalRef("ref", a.Ref)
}

func (a *ReportArgs) Validate() error {
	if err := checkLen("summary", a.Summary, MaxSummaryLen, true); err != nil {
		return err
	}
	if a.Status != ReportReview && a.Status != ReportDone {
		return argErr("status", "must be review or done")
	}
	return optionalNodeRef("node_id", a.NodeID)
}
