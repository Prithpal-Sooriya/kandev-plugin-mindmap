package contract

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"
)

// Structural validation. These checks need no map state; state-dependent
// checks (node exists, cycles, locks, claims, budgets) are in tree.go or are
// the backend's job.

func checkLen(field, s string, max int, required bool) error {
	if required && strings.TrimSpace(s) == "" {
		return FieldErr(ErrInvalidOp, field, "required")
	}
	if !utf8.ValidString(s) {
		return FieldErr(ErrInvalidOp, field, "not valid UTF-8")
	}
	if len(s) > max {
		return FieldErr(ErrTooLarge, field, "longer than %d bytes", max)
	}
	return nil
}

func checkID(field, prefix, id string) error {
	if !ValidID(prefix, id) {
		return FieldErr(ErrInvalidOp, field, "expected a %s_ id, got %q", prefix, id)
	}
	return nil
}

// ValidateTitle checks a node title.
func ValidateTitle(field, s string) error {
	if err := checkLen(field, s, MaxTitleLen, true); err != nil {
		return err
	}
	if strings.ContainsAny(s, "\n\r") {
		return FieldErr(ErrInvalidOp, field, "must be a single line")
	}
	return nil
}

// ValidateTags checks a tag list: lowercase-ish slugs, no duplicates.
func ValidateTags(field string, tags []string) error {
	if len(tags) > MaxTags {
		return FieldErr(ErrTooLarge, field, "more than %d tags", MaxTags)
	}
	seen := map[string]bool{}
	for i, t := range tags {
		f := fmt.Sprintf("%s[%d]", field, i)
		if err := checkLen(f, t, MaxTagLen, true); err != nil {
			return err
		}
		if strings.ContainsAny(t, " \t\n,#") {
			return FieldErr(ErrInvalidOp, f, "tags cannot contain spaces, commas or #")
		}
		if seen[t] {
			return FieldErr(ErrInvalidOp, f, "duplicate tag %q", t)
		}
		seen[t] = true
	}
	return nil
}

// ValidateConfidence checks 0 <= c <= 1.
func ValidateConfidence(field string, c *float64) error {
	if c != nil && (*c < 0 || *c > 1) {
		return FieldErr(ErrInvalidOp, field, "confidence must be between 0 and 1")
	}
	return nil
}

// ValidateHTTPURL checks an absolute http(s) URL.
func ValidateHTTPURL(field, s string) error {
	if err := checkLen(field, s, MaxURLLen, true); err != nil {
		return err
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return FieldErr(ErrInvalidOp, field, "must be an absolute http(s) URL")
	}
	return nil
}

// ValidateSources checks a source list.
func ValidateSources(field string, srcs []Source) error {
	if len(srcs) > MaxSources {
		return FieldErr(ErrTooLarge, field, "more than %d sources", MaxSources)
	}
	for i, s := range srcs {
		f := fmt.Sprintf("%s[%d]", field, i)
		if err := ValidateHTTPURL(f+".url", s.URL); err != nil {
			return err
		}
		if err := checkLen(f+".title", s.Title, MaxSourceTitleLen, false); err != nil {
			return err
		}
		if err := checkLen(f+".quote", s.Quote, MaxQuoteLen, false); err != nil {
			return err
		}
	}
	return nil
}

// ValidateOrderKey checks a fractional-index key (see order.go).
func ValidateOrderKey(field, k string) error {
	if !ValidOrderKey(k) {
		return FieldErr(ErrInvalidOp, field, "invalid order key %q", k)
	}
	return nil
}

// ValidateColor accepts #rgb/#rrggbb or a host CSS variable token like "accent".
func ValidateColor(field, c string) error {
	if c == "" {
		return nil
	}
	if len(c) > 32 || strings.ContainsAny(c, " ;:(){}\"'<>") {
		return FieldErr(ErrInvalidOp, field, "invalid colour %q", c)
	}
	return nil
}

// ValidateDocPath checks a relative markdown path: no leading slash, no "..",
// ends in .md.
func ValidateDocPath(field, p string) error {
	if err := checkLen(field, p, MaxDocPathLen, true); err != nil {
		return err
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || !strings.HasSuffix(p, ".md") {
		return FieldErr(ErrInvalidOp, field, "must be a relative .md path")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return FieldErr(ErrInvalidOp, field, "invalid path segment in %q", p)
		}
	}
	return nil
}

func (d *DocMeta) validate(field string) error {
	if d == nil {
		return nil
	}
	if err := ValidateDocPath(field+".path", d.Path); err != nil {
		return err
	}
	for i, id := range d.SourceIDs {
		if err := checkID(fmt.Sprintf("%s.source_ids[%d]", field, i), PrefixNode, id); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks a NodeInit on its own.
func (n *NodeInit) Validate() error {
	if err := checkID("node.id", PrefixNode, n.ID); err != nil {
		return err
	}
	if n.ParentID != "" {
		if err := checkID("node.parent_id", PrefixNode, n.ParentID); err != nil {
			return err
		}
	}
	if err := ValidateOrderKey("node.order", n.Order); err != nil {
		return err
	}
	if err := ValidateTitle("node.title", n.Title); err != nil {
		return err
	}
	if err := checkLen("node.body_md", n.BodyMD, MaxBodyLen, false); err != nil {
		return err
	}
	if !n.Kind.Valid() {
		return FieldErr(ErrInvalidOp, "node.kind", "unknown kind %q", n.Kind)
	}
	if n.Status != "" && !n.Status.Valid() {
		return FieldErr(ErrInvalidOp, "node.status", "unknown status %q", n.Status)
	}
	if err := ValidateTags("node.tags", n.Tags); err != nil {
		return err
	}
	if err := ValidateConfidence("node.confidence", n.Confidence); err != nil {
		return err
	}
	if err := ValidateSources("node.sources", n.Sources); err != nil {
		return err
	}
	if err := ValidateColor("node.color", n.Color); err != nil {
		return err
	}
	if (n.Kind == KindDoc) != (n.Doc != nil) {
		return FieldErr(ErrInvalidOp, "node.doc", "doc metadata is required for kind=doc and only allowed there")
	}
	if n.Question != nil && n.Kind != KindQuestion {
		return FieldErr(ErrInvalidOp, "node.question", "question payload requires kind=question")
	}
	return n.Doc.validate("node.doc")
}

func (p *MapUpdatePayload) Validate() error {
	if p.Title == nil && p.RepoID == nil && p.Settings == nil {
		return FieldErr(ErrInvalidOp, "payload", "nothing to update")
	}
	if p.Title != nil {
		if err := ValidateTitle("title", *p.Title); err != nil {
			return err
		}
	}
	if s := p.Settings; s != nil {
		if s.DefaultMode != "" && !s.DefaultMode.Valid() {
			return FieldErr(ErrInvalidOp, "settings.default_mode", "unknown mode %q", s.DefaultMode)
		}
		if s.MaxConcurrentRun < 0 {
			return FieldErr(ErrInvalidOp, "settings.max_concurrent_runs", "must be >= 0")
		}
		if err := s.DefaultBudget.Validate("settings.default_budget"); err != nil {
			return err
		}
	}
	return nil
}

func (p *NodeCreatePayload) Validate() error { return p.Node.Validate() }

func (p *NodeUpdatePayload) Validate() error {
	if err := checkID("id", PrefixNode, p.ID); err != nil {
		return err
	}
	if p.Set == nil && len(p.Clear) == 0 && p.AppendBody == "" && len(p.AddTags) == 0 &&
		len(p.RemoveTags) == 0 && len(p.AddSources) == 0 {
		return FieldErr(ErrInvalidOp, "payload", "nothing to update")
	}
	if s := p.Set; s != nil {
		if s.Title != nil {
			if err := ValidateTitle("set.title", *s.Title); err != nil {
				return err
			}
		}
		if s.BodyMD != nil {
			if err := checkLen("set.body_md", *s.BodyMD, MaxBodyLen, false); err != nil {
				return err
			}
		}
		if s.Kind != nil && !s.Kind.Valid() {
			return FieldErr(ErrInvalidOp, "set.kind", "unknown kind %q", *s.Kind)
		}
		if s.Tags != nil {
			if err := ValidateTags("set.tags", *s.Tags); err != nil {
				return err
			}
		}
		if err := ValidateConfidence("set.confidence", s.Confidence); err != nil {
			return err
		}
		if s.Status != nil && !s.Status.Valid() {
			return FieldErr(ErrInvalidOp, "set.status", "unknown status %q", *s.Status)
		}
		if s.RunID != nil && *s.RunID != "" {
			if err := checkID("set.run_id", PrefixRun, *s.RunID); err != nil {
				return err
			}
		}
		if s.Color != nil {
			if err := ValidateColor("set.color", *s.Color); err != nil {
				return err
			}
		}
		if err := s.Doc.validate("set.doc"); err != nil {
			return err
		}
	}
	for i, c := range p.Clear {
		if !slices.Contains(ClearableFields, c) {
			return FieldErr(ErrInvalidOp, fmt.Sprintf("clear[%d]", i), "field %q cannot be cleared", c)
		}
	}
	if err := checkLen("append_body", p.AppendBody, MaxBodyLen, false); err != nil {
		return err
	}
	if err := ValidateTags("add_tags", p.AddTags); err != nil {
		return err
	}
	if err := ValidateTags("remove_tags", p.RemoveTags); err != nil {
		return err
	}
	return ValidateSources("add_sources", p.AddSources)
}

func (p *NodeMovePayload) Validate() error {
	if err := checkID("id", PrefixNode, p.ID); err != nil {
		return err
	}
	if err := checkID("parent_id", PrefixNode, p.ParentID); err != nil {
		return err
	}
	if p.ID == p.ParentID {
		return FieldErr(ErrCycle, "parent_id", "a node cannot be its own parent")
	}
	return ValidateOrderKey("order", p.Order)
}

func (p *NodeDeletePayload) Validate() error  { return checkID("id", PrefixNode, p.ID) }
func (p *NodeRestorePayload) Validate() error { return checkID("id", PrefixNode, p.ID) }
func (p *NodeReleasePayload) Validate() error { return checkID("id", PrefixNode, p.ID) }

func (p *NodeMergePayload) Validate() error {
	if err := checkID("into_id", PrefixNode, p.IntoID); err != nil {
		return err
	}
	if len(p.IDs) == 0 || len(p.IDs) > MaxMergeSources {
		return FieldErr(ErrInvalidOp, "ids", "merge needs 1-%d nodes", MaxMergeSources)
	}
	seen := map[string]bool{}
	for i, id := range p.IDs {
		f := fmt.Sprintf("ids[%d]", i)
		if err := checkID(f, PrefixNode, id); err != nil {
			return err
		}
		if id == p.IntoID || seen[id] {
			return FieldErr(ErrInvalidOp, f, "duplicate or self merge of %s", id)
		}
		seen[id] = true
	}
	return nil
}

func (p *NodeClaimPayload) Validate() error {
	if err := checkID("id", PrefixNode, p.ID); err != nil {
		return err
	}
	if err := checkID("claim.participant_id", PrefixParticipant, p.Claim.ParticipantID); err != nil {
		return err
	}
	if p.Claim.ExpiresAt.IsZero() {
		return FieldErr(ErrInvalidOp, "claim.expires_at", "required")
	}
	return checkLen("claim.note", p.Claim.Note, MaxCaptionLen, false)
}

func (p *QuestionAnswerPayload) Validate() error {
	if err := checkID("node_id", PrefixNode, p.NodeID); err != nil {
		return err
	}
	return checkLen("answer", p.Answer, MaxQuestionLen, true)
}

func (p *LinkCreatePayload) Validate() error {
	l := p.Link
	if err := checkID("link.id", PrefixLink, l.ID); err != nil {
		return err
	}
	if err := checkID("link.from", PrefixNode, l.From); err != nil {
		return err
	}
	if err := checkID("link.to", PrefixNode, l.To); err != nil {
		return err
	}
	if l.From == l.To {
		return FieldErr(ErrInvalidOp, "link.to", "a link must join two different nodes")
	}
	if err := checkLen("link.label", l.Label, MaxLinkLabelLen, false); err != nil {
		return err
	}
	return checkLen("link.reason", l.Reason, MaxLinkReasonLen, false)
}

func (p *LinkUpdatePayload) Validate() error {
	if err := checkID("id", PrefixLink, p.ID); err != nil {
		return err
	}
	if p.Label == nil && p.Reason == nil {
		return FieldErr(ErrInvalidOp, "payload", "nothing to update")
	}
	if p.Label != nil {
		if err := checkLen("label", *p.Label, MaxLinkLabelLen, false); err != nil {
			return err
		}
	}
	if p.Reason != nil {
		return checkLen("reason", *p.Reason, MaxLinkReasonLen, false)
	}
	return nil
}

func (p *LinkDeletePayload) Validate() error { return checkID("id", PrefixLink, p.ID) }

func (p *AssetAttachPayload) Validate() error {
	a := p.Asset
	if err := checkID("asset.id", PrefixAsset, a.ID); err != nil {
		return err
	}
	if err := checkID("asset.node_id", PrefixNode, a.NodeID); err != nil {
		return err
	}
	kind, ok := AssetKindForMIME(a.MIME)
	if !ok {
		return FieldErr(ErrUnsupportedMedia, "asset.mime", "MIME type %q not allowed", a.MIME)
	}
	if a.Kind != kind {
		return FieldErr(ErrInvalidOp, "asset.kind", "kind %q does not match MIME %q", a.Kind, a.MIME)
	}
	if a.Bytes <= 0 || a.Bytes > MaxAssetBytes {
		return FieldErr(ErrTooLarge, "asset.bytes", "asset must be 1..%d bytes", MaxAssetBytes)
	}
	if len(a.SHA256) != 64 {
		return FieldErr(ErrInvalidOp, "asset.sha256", "expected a hex SHA-256")
	}
	if a.SourceURL != "" {
		if err := ValidateHTTPURL("asset.source_url", a.SourceURL); err != nil {
			return err
		}
	}
	return checkLen("asset.caption", a.Caption, MaxCaptionLen, false)
}

func (p *AssetDetachPayload) Validate() error {
	if err := checkID("id", PrefixAsset, p.ID); err != nil {
		return err
	}
	return checkID("node_id", PrefixNode, p.NodeID)
}

// Validate checks a Budget. A nil budget is valid (inherit).
func (b *Budget) Validate(field string) error {
	if b == nil {
		return nil
	}
	if b.MaxDepth < 0 || b.MaxNodes < 0 || b.MaxSubagents < 0 || b.MaxMinutes < 0 {
		return FieldErr(ErrInvalidOp, field, "budget values must be >= 0")
	}
	return nil
}

// Validate checks the op envelope and its payload without map state. It is
// the first thing the server runs on every submitted op, and what the UI can
// run before sending.
func (o *Op) Validate() error {
	if err := checkID("id", PrefixOp, o.ID); err != nil {
		return err
	}
	if err := checkID("map_id", PrefixMap, o.MapID); err != nil {
		return err
	}
	if o.Rev < 0 || o.BaseRev < 0 {
		return FieldErr(ErrInvalidOp, "rev", "revs are never negative")
	}
	if o.Payload == nil {
		return FieldErr(ErrInvalidOp, "payload", "missing payload")
	}
	if o.Type != o.Payload.OpType() {
		return FieldErr(ErrInvalidOp, "type", "type %q does not match payload", o.Type)
	}
	if o.ProposalID != "" {
		if err := checkID("proposal_id", PrefixProposal, o.ProposalID); err != nil {
			return err
		}
	}
	if o.BatchID != "" {
		if err := checkID("batch_id", PrefixBatch, o.BatchID); err != nil {
			return err
		}
	}
	return o.Payload.Validate()
}
