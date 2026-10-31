package contract

import (
	"encoding/json"
	"time"
)

// WebSocket (and long-poll) messages. Every frame is one JSON object
// {"type": <MsgType>, "payload": {...}}. The same ServerMessage values are
// returned in batches by the long-poll fallback, so a client has one reducer
// for both transports. See docs/protocol.md for ordering rules.

type MsgType string

// Client -> server.
const (
	MsgHello             MsgType = "hello"
	MsgSubmitOps         MsgType = "submit"
	MsgPresenceUpdate    MsgType = "presence.update"
	MsgOutputSubscribe   MsgType = "output.subscribe"
	MsgOutputUnsubscribe MsgType = "output.unsubscribe"
	MsgPing              MsgType = "ping"
)

// Server -> client.
const (
	MsgWelcome     MsgType = "welcome"
	MsgSnapshot    MsgType = "snapshot"
	MsgOps         MsgType = "ops"
	MsgAck         MsgType = "ack"
	MsgReject      MsgType = "reject"
	MsgRebase      MsgType = "rebase"
	MsgPresence    MsgType = "presence"
	MsgParticipant MsgType = "participant"
	MsgRun         MsgType = "run"
	MsgProposal    MsgType = "proposal"
	MsgActivity    MsgType = "activity"
	MsgOutput      MsgType = "output"
	MsgError       MsgType = "error"
	MsgPong        MsgType = "pong"
)

var ClientMsgTypes = []MsgType{MsgHello, MsgSubmitOps, MsgPresenceUpdate, MsgOutputSubscribe, MsgOutputUnsubscribe, MsgPing}

var ServerMsgTypes = []MsgType{
	MsgWelcome, MsgSnapshot, MsgOps, MsgAck, MsgReject, MsgRebase, MsgPresence, MsgParticipant,
	MsgRun, MsgProposal, MsgActivity, MsgOutput, MsgError, MsgPong,
}

// Message is one WS frame or one long-poll entry.
type Message struct {
	Type    MsgType
	Payload any // pointer to the payload struct for Type
}

type messageWire struct {
	Type    MsgType         `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// NewMsgPayload returns a pointer to the zero payload for t, or nil.
func NewMsgPayload(t MsgType) any {
	switch t {
	case MsgHello:
		return &HelloPayload{}
	case MsgSubmitOps:
		return &SubmitPayload{}
	case MsgPresenceUpdate:
		return &PresenceUpdatePayload{}
	case MsgOutputSubscribe, MsgOutputUnsubscribe:
		return &OutputSubscriptionPayload{}
	case MsgPing, MsgPong:
		return &PingPayload{}
	case MsgWelcome:
		return &WelcomePayload{}
	case MsgSnapshot:
		return &Snapshot{}
	case MsgOps:
		return &OpsPayload{}
	case MsgAck:
		return &AckPayload{}
	case MsgReject:
		return &RejectPayload{}
	case MsgRebase:
		return &RebasePayload{}
	case MsgPresence:
		return &PresencePayload{}
	case MsgParticipant:
		return &ParticipantPayload{}
	case MsgRun:
		return &RunPayload{}
	case MsgProposal:
		return &ProposalPayload{}
	case MsgActivity:
		return &ActivityPayload{}
	case MsgOutput:
		return &OutputPayload{}
	case MsgError:
		return &ErrorPayload{}
	}
	return nil
}

func (m Message) MarshalJSON() ([]byte, error) {
	raw, err := json.Marshal(m.Payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(messageWire{m.Type, raw})
}

func (m *Message) UnmarshalJSON(b []byte) error {
	var w messageWire
	if err := strictDecode(b, &w); err != nil {
		return err
	}
	p := NewMsgPayload(w.Type)
	if p == nil {
		return FieldErr(ErrBadRequest, "type", "unknown message type %q", w.Type)
	}
	if len(w.Payload) == 0 {
		w.Payload = []byte("{}")
	}
	if err := strictDecode(w.Payload, p); err != nil {
		return FieldErr(ErrBadRequest, "payload", "%v", err)
	}
	*m = Message{Type: w.Type, Payload: p}
	return nil
}

// --- client payloads ------------------------------------------------------------

// HelloPayload is the first client frame. A client that has state cached at
// SinceRev may ask for a catch-up instead of a snapshot; the server answers
// with `ops` from SinceRev+1 if it still has them, else with a snapshot.
type HelloPayload struct {
	Contract int `json:"contract"`
	// ClientID is a random per-tab id; it lets the server drop echo presence.
	ClientID string `json:"client_id"`
	// Resume re-attaches to an existing participant (guest cookie or saved id).
	ParticipantID string `json:"participant_id,omitempty"`
	// Name and Color are used when creating a new guest participant.
	Name     string `json:"name,omitempty"`
	Color    string `json:"color,omitempty"`
	SinceRev int64  `json:"since_rev,omitempty"`
}

// SubmitPayload carries client ops. All ops in one submit are applied
// atomically, in order, as one batch.
type SubmitPayload struct {
	Ops []Op `json:"ops"`
}

type PresenceUpdatePayload struct {
	Presence Presence `json:"presence"`
}

type OutputSubscriptionPayload struct {
	RunID string `json:"run_id"`
}

type PingPayload struct {
	Nonce string `json:"nonce,omitempty"`
}

// --- server payloads ------------------------------------------------------------

type WelcomePayload struct {
	Contract    int         `json:"contract"`
	Participant Participant `json:"participant"`
	ServerTime  time.Time   `json:"server_time"`
	// Cursor is the long-poll cursor at the moment of welcome (see SyncResponse).
	Cursor string `json:"cursor,omitempty"`
}

// Snapshot is the full live state of one map at Rev. Tombstoned nodes and
// links are omitted. Activity holds the latest events (newest last, <= 50).
type Snapshot struct {
	Rev          int64           `json:"rev"`
	Map          Map             `json:"map"`
	Nodes        []Node          `json:"nodes"`
	Links        []Link          `json:"links"`
	Runs         []Run           `json:"runs"`
	Proposals    []Proposal      `json:"proposals"`
	Participants []Participant   `json:"participants"`
	Presence     []Presence      `json:"presence"`
	Activity     []ActivityEvent `json:"activity"`
}

// OpsPayload broadcasts committed ops in rev order. Rev is the map rev after
// the last op. A client's own ops are not echoed back; it gets `ack`.
type OpsPayload struct {
	Ops []Op  `json:"ops"`
	Rev int64 `json:"rev"`
}

// AckPayload confirms a submit: every op committed unchanged, at these revs.
type AckPayload struct {
	Acks []OpAck `json:"acks"`
}

type OpAck struct {
	ID  string `json:"id"`
	Rev int64  `json:"rev"`
}

// RejectPayload: the whole submit batch was refused; the client drops the
// optimistic ops in IDs and re-derives state from the server.
type RejectPayload struct {
	IDs   []string `json:"ids"`
	Error Error    `json:"error"`
}

// RebasePayload: the op committed but the server changed it (e.g. rewrote a
// colliding order key, clamped a field, re-pointed a merge). The client
// replaces its optimistic op with Op.
type RebasePayload struct {
	Op Op `json:"op"`
}

// PresencePayload carries changed presence entries and participants who left.
type PresencePayload struct {
	Presence []Presence `json:"presence,omitempty"`
	Left     []string   `json:"left,omitempty"`
}

type ParticipantPayload struct {
	Participant Participant `json:"participant"`
}

type RunPayload struct {
	Run Run `json:"run"`
}

type ProposalPayload struct {
	Proposal Proposal `json:"proposal"`
}

type ActivityPayload struct {
	Events []ActivityEvent `json:"events"`
}

// OutputLineKind classifies agent output for the inspector stream.
type OutputLineKind string

const (
	OutputText     OutputLineKind = "text"
	OutputThinking OutputLineKind = "thinking"
	OutputTool     OutputLineKind = "tool"
	OutputStatus   OutputLineKind = "status"
)

var OutputLineKinds = []OutputLineKind{OutputText, OutputThinking, OutputTool, OutputStatus}

type OutputLine struct {
	TS   time.Time      `json:"ts"`
	Kind OutputLineKind `json:"kind"`
	Text string         `json:"text"`
}

// OutputPayload streams agent output for a subscribed run. Every client also
// sees the latest line via Run.LastOutput without subscribing.
type OutputPayload struct {
	RunID string       `json:"run_id"`
	Lines []OutputLine `json:"lines"`
}

// ErrorPayload reports a connection-level error. Fatal errors are followed by
// a close; code `resync` means "send hello again without since_rev".
type ErrorPayload struct {
	Error Error `json:"error"`
	Fatal bool  `json:"fatal,omitempty"`
}
