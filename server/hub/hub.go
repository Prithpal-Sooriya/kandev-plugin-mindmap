// Package hub fans server messages out to the clients of each map: WebSocket
// connections (through their outbound queues) and long-poll readers (through
// a per-map ring buffer addressed by cursor). It also owns ephemeral
// presence and the soft locks derived from it.
package hub

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"kandev-plugin-mindmap/server/contract"
)

// RingSize is how many messages each map keeps for long-poll catch-up.
const RingSize = 2000

// presenceFlush is how often coalesced presence changes are broadcast.
const presenceFlush = 50 * time.Millisecond

// Client is one WebSocket connection. Out is drained by the connection's
// writer goroutine; when it is full the client is too slow and is dropped.
type Client struct {
	ConnID      string
	ClientID    string
	MapID       string
	Participant contract.Participant
	Out         chan contract.Message

	mu      sync.Mutex
	subs    map[string]bool
	closed  bool
	dropped chan struct{}
}

// NewClient makes a client with a buffered outbound queue.
func NewClient(connID, clientID, mapID string, p contract.Participant) *Client {
	return &Client{ConnID: connID, ClientID: clientID, MapID: mapID, Participant: p,
		Out: make(chan contract.Message, 512), subs: map[string]bool{}, dropped: make(chan struct{})}
}

// Dropped is closed when the hub gives up on a slow client.
func (c *Client) Dropped() <-chan struct{} { return c.dropped }

// Send queues m. It returns false (and drops the client) if the queue is full.
func (c *Client) Send(m contract.Message) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	select {
	case c.Out <- m:
		return true
	default:
		c.closed = true
		close(c.dropped)
		return false
	}
}

// Subscribe toggles output streaming for a run.
func (c *Client) Subscribe(runID string, on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if on {
		c.subs[runID] = true
	} else {
		delete(c.subs, runID)
	}
}

func (c *Client) subscribed(runID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subs[runID]
}

type entry struct {
	seq    int64
	msg    contract.Message
	origin string // client id of the sender (ops), so long-poll can skip echoes
	runID  string // output messages: only for subscribers
}

type room struct {
	clients  map[*Client]bool
	presence map[string]contract.Presence
	dirty    map[string]bool
	left     []string
	ring     []entry
	seq      int64
	rev      int64
	notify   chan struct{} // closed and replaced on every append
}

// Hub is safe for concurrent use.
type Hub struct {
	mu    sync.Mutex
	rooms map[string]*room
	now   func() time.Time
	stop  chan struct{}
	once  sync.Once
}

// New starts a hub and its presence flusher.
func New() *Hub {
	h := &Hub{rooms: map[string]*room{}, now: func() time.Time { return time.Now().UTC() }, stop: make(chan struct{})}
	go h.flushLoop()
	return h
}

// Close stops the flusher.
func (h *Hub) Close() { h.once.Do(func() { close(h.stop) }) }

func (h *Hub) room(mapID string) *room {
	r := h.rooms[mapID]
	if r == nil {
		r = &room{clients: map[*Client]bool{}, presence: map[string]contract.Presence{}, dirty: map[string]bool{}, notify: make(chan struct{})}
		h.rooms[mapID] = r
	}
	return r
}

func (r *room) append(e entry) {
	r.seq++
	e.seq = r.seq
	r.ring = append(r.ring, e)
	if len(r.ring) > RingSize {
		r.ring = append(r.ring[:0:0], r.ring[len(r.ring)-RingSize:]...)
	}
	close(r.notify)
	r.notify = make(chan struct{})
}

// Join registers a client. The caller sends welcome/snapshot itself.
func (h *Hub) Join(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.room(c.MapID).clients[c] = true
}

// Leave unregisters a client. When it was the participant's last connection
// its presence is dropped and broadcast as left.
func (h *Hub) Leave(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.room(c.MapID)
	delete(r.clients, c)
	for o := range r.clients {
		if o.Participant.ID == c.Participant.ID {
			return
		}
	}
	if _, ok := r.presence[c.Participant.ID]; ok {
		delete(r.presence, c.Participant.ID)
		delete(r.dirty, c.Participant.ID)
		r.left = append(r.left, c.Participant.ID)
	}
}

// Online lists the participants with at least one open connection.
func (h *Hub) Online(mapID string) map[string]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]bool{}
	for c := range h.room(mapID).clients {
		out[c.Participant.ID] = true
	}
	return out
}

// Cursor returns the long-poll cursor for "now" on a map.
func (h *Hub) Cursor(mapID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.room(mapID)
	return FormatCursor(r.rev, r.seq)
}

// FormatCursor renders the opaque cursor c<rev>.<seq>.
func FormatCursor(rev, seq int64) string { return fmt.Sprintf("c%d.%d", rev, seq) }

// ParseCursor splits a cursor; ok is false when malformed.
func ParseCursor(c string) (rev, seq int64, ok bool) {
	if !strings.HasPrefix(c, "c") {
		return 0, 0, false
	}
	a, b, found := strings.Cut(c[1:], ".")
	if !found {
		return 0, 0, false
	}
	rev, err1 := strconv.ParseInt(a, 10, 64)
	seq, err2 := strconv.ParseInt(b, 10, 64)
	return rev, seq, err1 == nil && err2 == nil && rev >= 0 && seq >= 0
}

// SetRev records the map's head rev (for cursors) without broadcasting.
func (h *Hub) SetRev(mapID string, rev int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.room(mapID); rev > r.rev {
		r.rev = rev
	}
}

// PublishOps broadcasts committed ops to every connection of the map except
// the one that submitted them (origin conn), and records them for long-poll.
// Callers publish in commit order while holding their submit ordering, so
// every client sees ops in rev order and before its own ack.
func (h *Hub) PublishOps(mapID string, ops []contract.Op, rev int64, originConn, originClient string) {
	if len(ops) == 0 {
		return
	}
	m := contract.Message{Type: contract.MsgOps, Payload: &contract.OpsPayload{Ops: ops, Rev: rev}}
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.room(mapID)
	if rev > r.rev {
		r.rev = rev
	}
	r.append(entry{msg: m, origin: originClient})
	for c := range r.clients {
		if c.ConnID != originConn {
			c.Send(m)
		}
	}
}

// Publish broadcasts a non-op message (run, proposal, activity,
// participant, ...) to every connection and records it for long-poll.
func (h *Hub) Publish(mapID string, m contract.Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.room(mapID)
	r.append(entry{msg: m})
	for c := range r.clients {
		c.Send(m)
	}
}

// PublishOutput sends agent output lines to subscribers of runID only.
func (h *Hub) PublishOutput(mapID, runID string, lines []contract.OutputLine) {
	m := contract.Message{Type: contract.MsgOutput, Payload: &contract.OutputPayload{RunID: runID, Lines: lines}}
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.room(mapID)
	r.append(entry{msg: m, runID: runID})
	for c := range r.clients {
		if c.subscribed(runID) {
			c.Send(m)
		}
	}
}

// UpdatePresence replaces one participant's presence; the change is
// broadcast by the next flush (at most every 50 ms).
func (h *Hub) UpdatePresence(mapID string, p contract.Presence) {
	p.UpdatedAt = h.now()
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.room(mapID)
	r.presence[p.ParticipantID] = p
	r.dirty[p.ParticipantID] = true
}

// Presence returns the live presence entries of a map.
func (h *Hub) Presence(mapID string) []contract.Presence {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []contract.Presence{}
	for _, p := range h.room(mapID).presence {
		out = append(out, p)
	}
	return out
}

// Locked reports soft locks from presence: field of nodeID is locked if a
// participant other than by is editing it and the lock has not expired.
func (h *Hub) Locked(mapID string) func(nodeID string, field contract.LockField, by string) bool {
	return func(nodeID string, field contract.LockField, by string) bool {
		now := h.now()
		h.mu.Lock()
		defer h.mu.Unlock()
		for pid, p := range h.room(mapID).presence {
			e := p.Editing
			if pid != by && e != nil && e.NodeID == nodeID && e.Field == field && now.Before(e.ExpiresAt) && e.ExpiresAt.Sub(now) <= 2*contract.SoftLockTTL {
				return true
			}
		}
		return false
	}
}

func (h *Hub) flushLoop() {
	t := time.NewTicker(presenceFlush)
	defer t.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-t.C:
			h.flushPresence()
		}
	}
}

func (h *Hub) flushPresence() {
	now := h.now()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.rooms {
		for pid, p := range r.presence {
			if now.Sub(p.UpdatedAt) > contract.PresenceTimeout {
				delete(r.presence, pid)
				delete(r.dirty, pid)
				r.left = append(r.left, pid)
			}
		}
		if len(r.dirty) == 0 && len(r.left) == 0 {
			continue
		}
		pl := &contract.PresencePayload{Left: r.left}
		for pid := range r.dirty {
			pl.Presence = append(pl.Presence, r.presence[pid])
		}
		r.dirty, r.left = map[string]bool{}, nil
		m := contract.Message{Type: contract.MsgPresence, Payload: pl}
		r.append(entry{msg: m})
		for c := range r.clients {
			c.Send(m)
		}
	}
}

// SyncResult is what Wait found after a cursor.
type SyncResult struct {
	Messages []contract.Message
	Cursor   string
	// Behind is set when the cursor is unknown to this process (restart) or
	// older than the ring: the caller must catch up from FromRev via the op
	// log, or send a snapshot when FromRev is 0.
	Behind  bool
	FromRev int64
}

// Wait returns the messages after cursor, holding up to wait for at least
// one. originClient's own ops are skipped; output only for subs.
func (h *Hub) Wait(ctx context.Context, mapID, cursor string, wait time.Duration, originClient string, subs map[string]bool) SyncResult {
	rev, seq, ok := ParseCursor(cursor)
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		h.mu.Lock()
		r := h.room(mapID)
		if !ok || seq > r.seq || (len(r.ring) > 0 && seq < r.ring[0].seq-1) || (len(r.ring) == 0 && seq != r.seq) {
			res := SyncResult{Behind: true, FromRev: rev, Cursor: FormatCursor(r.rev, r.seq)}
			if !ok {
				res.FromRev = 0
			}
			h.mu.Unlock()
			return res
		}
		var msgs []contract.Message
		for _, e := range r.ring {
			if e.seq <= seq {
				continue
			}
			if e.origin != "" && e.origin == originClient {
				continue
			}
			if e.runID != "" && !subs[e.runID] {
				continue
			}
			msgs = append(msgs, e.msg)
		}
		cur := FormatCursor(r.rev, r.seq)
		notify := r.notify
		h.mu.Unlock()
		if len(msgs) > 0 || wait <= 0 {
			if msgs == nil {
				msgs = []contract.Message{}
			}
			return SyncResult{Messages: msgs, Cursor: cur}
		}
		_, seq, _ = ParseCursor(cur)
		select {
		case <-notify:
		case <-deadline.C:
			return SyncResult{Messages: []contract.Message{}, Cursor: cur}
		case <-ctx.Done():
			return SyncResult{Messages: []contract.Message{}, Cursor: cur}
		}
	}
}
