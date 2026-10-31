package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// OpType names one primitive map mutation. Every change to persistent map
// state is exactly one op in the per-map log; composite actions (agent tool
// calls, regroup, accept proposal) expand into several ops sharing a batch_id.
type OpType string

const (
	OpMapUpdate      OpType = "map.update"
	OpNodeCreate     OpType = "node.create"
	OpNodeUpdate     OpType = "node.update"
	OpNodeMove       OpType = "node.move"
	OpNodeDelete     OpType = "node.delete"  // tombstones the node and its subtree
	OpNodeRestore    OpType = "node.restore" // un-tombstones a node and its subtree (undo)
	OpNodeMerge      OpType = "node.merge"
	OpNodeClaim      OpType = "node.claim"
	OpNodeRelease    OpType = "node.release"
	OpQuestionAnswer OpType = "question.answer"
	OpLinkCreate     OpType = "link.create"
	OpLinkUpdate     OpType = "link.update"
	OpLinkDelete     OpType = "link.delete"
	OpAssetAttach    OpType = "asset.attach"
	OpAssetDetach    OpType = "asset.detach"
)

var OpTypes = []OpType{
	OpMapUpdate, OpNodeCreate, OpNodeUpdate, OpNodeMove, OpNodeDelete, OpNodeRestore, OpNodeMerge,
	OpNodeClaim, OpNodeRelease, OpQuestionAnswer, OpLinkCreate, OpLinkUpdate, OpLinkDelete,
	OpAssetAttach, OpAssetDetach,
}

// OpPayload is implemented by every op payload struct. It is the tagged union
// of Op.Payload; the tag is Op.Type.
type OpPayload interface {
	OpType() OpType
	// Validate checks the payload on its own (no map state needed).
	Validate() error
}

// NewOpPayload returns a zero payload for t, or nil if t is unknown.
func NewOpPayload(t OpType) OpPayload {
	switch t {
	case OpMapUpdate:
		return &MapUpdatePayload{}
	case OpNodeCreate:
		return &NodeCreatePayload{}
	case OpNodeUpdate:
		return &NodeUpdatePayload{}
	case OpNodeMove:
		return &NodeMovePayload{}
	case OpNodeDelete:
		return &NodeDeletePayload{}
	case OpNodeRestore:
		return &NodeRestorePayload{}
	case OpNodeMerge:
		return &NodeMergePayload{}
	case OpNodeClaim:
		return &NodeClaimPayload{}
	case OpNodeRelease:
		return &NodeReleasePayload{}
	case OpQuestionAnswer:
		return &QuestionAnswerPayload{}
	case OpLinkCreate:
		return &LinkCreatePayload{}
	case OpLinkUpdate:
		return &LinkUpdatePayload{}
	case OpLinkDelete:
		return &LinkDeletePayload{}
	case OpAssetAttach:
		return &AssetAttachPayload{}
	case OpAssetDetach:
		return &AssetDetachPayload{}
	}
	return nil
}

// Op is one entry of the per-map op log, and the unit clients submit.
//
// Clients mint ID (op_<random>) and leave Rev at 0. The server assigns Rev
// (strictly increasing per map, no gaps) and TS on commit, and overwrites
// Actor from the authenticated connection.
type Op struct {
	ID         string    `json:"id"`
	MapID      string    `json:"map_id"`
	Rev        int64     `json:"rev"`
	Type       OpType    `json:"type"`
	Payload    OpPayload `json:"payload"`
	Actor      Actor     `json:"actor"`
	ProposalID string    `json:"proposal_id,omitempty"` // set when applied from an accepted proposal, or while pending in one
	BatchID    string    `json:"batch_id,omitempty"`    // ops committed atomically together
	BaseRev    int64     `json:"base_rev,omitempty"`    // client's rev when it made the op (informational)
	TS         time.Time `json:"ts,omitzero"`
}

type opWire struct {
	ID         string          `json:"id"`
	MapID      string          `json:"map_id"`
	Rev        int64           `json:"rev"`
	Type       OpType          `json:"type"`
	Payload    json.RawMessage `json:"payload"`
	Actor      Actor           `json:"actor"`
	ProposalID string          `json:"proposal_id,omitempty"`
	BatchID    string          `json:"batch_id,omitempty"`
	BaseRev    int64           `json:"base_rev,omitempty"`
	TS         time.Time       `json:"ts,omitzero"`
}

// MarshalJSON encodes the payload under "payload" with "type" as the tag.
func (o Op) MarshalJSON() ([]byte, error) {
	if o.Payload == nil {
		return nil, fmt.Errorf("op %s: nil payload", o.ID)
	}
	if o.Type == "" {
		o.Type = o.Payload.OpType()
	}
	if o.Type != o.Payload.OpType() {
		return nil, fmt.Errorf("op %s: type %q does not match payload %q", o.ID, o.Type, o.Payload.OpType())
	}
	raw, err := json.Marshal(o.Payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(opWire{o.ID, o.MapID, o.Rev, o.Type, raw, o.Actor, o.ProposalID, o.BatchID, o.BaseRev, o.TS})
}

// UnmarshalJSON decodes the payload strictly (unknown fields rejected) into
// the struct selected by "type".
func (o *Op) UnmarshalJSON(b []byte) error {
	var w opWire
	if err := strictDecode(b, &w); err != nil {
		return err
	}
	p := NewOpPayload(w.Type)
	if p == nil {
		return FieldErr(ErrInvalidOp, "type", "unknown op type %q", w.Type)
	}
	if len(w.Payload) == 0 {
		return FieldErr(ErrInvalidOp, "payload", "missing payload")
	}
	if err := strictDecode(w.Payload, p); err != nil {
		return FieldErr(ErrInvalidOp, "payload", "%v", err)
	}
	*o = Op{w.ID, w.MapID, w.Rev, w.Type, p, w.Actor, w.ProposalID, w.BatchID, w.BaseRev, w.TS}
	return nil
}

func strictDecode(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing data after JSON value")
	}
	return nil
}

// StrictUnmarshal decodes JSON rejecting unknown fields. Use it for every
// inbound client and tool payload.
func StrictUnmarshal(b []byte, v any) error { return strictDecode(b, v) }

// --- payloads ---------------------------------------------------------------

// MapUpdatePayload sets map-level fields. Settings replaces the whole object.
type MapUpdatePayload struct {
	Title    *string      `json:"title,omitempty"`
	RepoID   *string      `json:"repo_id,omitempty"` // "" unlinks
	Settings *MapSettings `json:"settings,omitempty"`
}

// NodeInit is the client-settable part of a new node. Server-owned fields
// (map_id, author, rev, timestamps, status unless given) are filled on commit.
type NodeInit struct {
	ID         string     `json:"id"`
	ParentID   string     `json:"parent_id,omitempty"` // empty only for the map's first (root) node
	Order      string     `json:"order"`
	Title      string     `json:"title"`
	BodyMD     string     `json:"body_md,omitempty"`
	Kind       NodeKind   `json:"kind"`
	Tags       []string   `json:"tags,omitempty"`
	Confidence *float64   `json:"confidence,omitempty"`
	Sources    []Source   `json:"sources,omitempty"`
	Status     NodeStatus `json:"status,omitempty"` // default idle
	Color      string     `json:"color,omitempty"`
	Collapsed  bool       `json:"collapsed,omitempty"`
	Question   *Question  `json:"question,omitempty"`
	Doc        *DocMeta   `json:"doc,omitempty"`
}

type NodeCreatePayload struct {
	Node NodeInit `json:"node"`
}

// NodeFields is a partial node: every non-nil field is set. Each field is an
// independent last-writer-wins register (see docs/protocol.md).
type NodeFields struct {
	Title      *string     `json:"title,omitempty"`
	BodyMD     *string     `json:"body_md,omitempty"`
	Kind       *NodeKind   `json:"kind,omitempty"`
	Tags       *[]string   `json:"tags,omitempty"` // replaces the whole set
	Confidence *float64    `json:"confidence,omitempty"`
	Status     *NodeStatus `json:"status,omitempty"`
	RunID      *string     `json:"run_id,omitempty"`
	Color      *string     `json:"color,omitempty"`
	Collapsed  *bool       `json:"collapsed,omitempty"`
	Doc        *DocMeta    `json:"doc,omitempty"`
}

// ClearableField names a node field that node.update can unset.
type ClearableField string

const (
	ClearConfidence ClearableField = "confidence"
	ClearColor      ClearableField = "color"
	ClearRunID      ClearableField = "run_id"
	ClearBody       ClearableField = "body_md"
)

var ClearableFields = []ClearableField{ClearConfidence, ClearColor, ClearRunID, ClearBody}

// NodeUpdatePayload edits one node. Set, Clear and the merge-style fields
// (AppendBody, AddTags, RemoveTags, AddSources) may be combined; the server
// applies Set, then Clear, then the merge-style fields.
type NodeUpdatePayload struct {
	ID         string           `json:"id"`
	Set        *NodeFields      `json:"set,omitempty"`
	Clear      []ClearableField `json:"clear,omitempty"`
	AppendBody string           `json:"append_body,omitempty"` // appended after a blank line
	AddTags    []string         `json:"add_tags,omitempty"`
	RemoveTags []string         `json:"remove_tags,omitempty"`
	AddSources []Source         `json:"add_sources,omitempty"` // deduped by URL
}

// NodeMovePayload re-parents and/or reorders a node (with its subtree).
type NodeMovePayload struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id"`
	Order    string `json:"order"`
}

type NodeDeletePayload struct {
	ID string `json:"id"`
}

type NodeRestorePayload struct {
	ID string `json:"id"`
}

// NodeMergePayload folds IDs into IntoID: bodies appended (with a heading per
// merged node), tags and sources unioned, children re-parented under IntoID
// after its existing children, links rewired to IntoID, merged nodes
// tombstoned.
type NodeMergePayload struct {
	IntoID string   `json:"into_id"`
	IDs    []string `json:"ids"`
}

type NodeClaimPayload struct {
	ID    string `json:"id"`
	Claim Claim  `json:"claim"`
}

type NodeReleasePayload struct {
	ID string `json:"id"`
}

// QuestionAnswerPayload answers an ask_human question node. The server relays
// the answer to the asking run via Messages().Send.
type QuestionAnswerPayload struct {
	NodeID string `json:"node_id"`
	Answer string `json:"answer"`
}

type LinkInit struct {
	ID     string `json:"id"`
	From   string `json:"from"`
	To     string `json:"to"`
	Label  string `json:"label,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type LinkCreatePayload struct {
	Link LinkInit `json:"link"`
}

type LinkUpdatePayload struct {
	ID     string  `json:"id"`
	Label  *string `json:"label,omitempty"`
	Reason *string `json:"reason,omitempty"`
}

type LinkDeletePayload struct {
	ID string `json:"id"`
}

// AssetAttachPayload references bytes already stored by an upload or by the
// attach tool. It never carries bytes.
type AssetAttachPayload struct {
	Asset Asset `json:"asset"`
}

type AssetDetachPayload struct {
	ID     string `json:"id"`
	NodeID string `json:"node_id"`
}

func (*MapUpdatePayload) OpType() OpType      { return OpMapUpdate }
func (*NodeCreatePayload) OpType() OpType     { return OpNodeCreate }
func (*NodeUpdatePayload) OpType() OpType     { return OpNodeUpdate }
func (*NodeMovePayload) OpType() OpType       { return OpNodeMove }
func (*NodeDeletePayload) OpType() OpType     { return OpNodeDelete }
func (*NodeRestorePayload) OpType() OpType    { return OpNodeRestore }
func (*NodeMergePayload) OpType() OpType      { return OpNodeMerge }
func (*NodeClaimPayload) OpType() OpType      { return OpNodeClaim }
func (*NodeReleasePayload) OpType() OpType    { return OpNodeRelease }
func (*QuestionAnswerPayload) OpType() OpType { return OpQuestionAnswer }
func (*LinkCreatePayload) OpType() OpType     { return OpLinkCreate }
func (*LinkUpdatePayload) OpType() OpType     { return OpLinkUpdate }
func (*LinkDeletePayload) OpType() OpType     { return OpLinkDelete }
func (*AssetAttachPayload) OpType() OpType    { return OpAssetAttach }
func (*AssetDetachPayload) OpType() OpType    { return OpAssetDetach }

// TouchedNodeIDs lists the node IDs an op reads or writes, for proposal
// grouping, soft-lock checks and write trails.
func (o *Op) TouchedNodeIDs() []string {
	switch p := o.Payload.(type) {
	case *NodeCreatePayload:
		if p.Node.ParentID != "" {
			return []string{p.Node.ID, p.Node.ParentID}
		}
		return []string{p.Node.ID}
	case *NodeUpdatePayload:
		return []string{p.ID}
	case *NodeMovePayload:
		return []string{p.ID, p.ParentID}
	case *NodeDeletePayload:
		return []string{p.ID}
	case *NodeRestorePayload:
		return []string{p.ID}
	case *NodeMergePayload:
		return append([]string{p.IntoID}, p.IDs...)
	case *NodeClaimPayload:
		return []string{p.ID}
	case *NodeReleasePayload:
		return []string{p.ID}
	case *QuestionAnswerPayload:
		return []string{p.NodeID}
	case *LinkCreatePayload:
		return []string{p.Link.From, p.Link.To}
	case *AssetAttachPayload:
		return []string{p.Asset.NodeID}
	case *AssetDetachPayload:
		return []string{p.NodeID}
	}
	return nil
}
