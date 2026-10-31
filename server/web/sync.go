package web

import (
	"context"
	"strconv"
	"strings"
	"time"

	"kandev-plugin-mindmap/server/app"
	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/store"
)

// maxCatchUpOps is the largest gap served as ops; beyond it a snapshot is
// cheaper for the client.
const maxCatchUpOps = 1000

// SyncQuery is the long-poll request (GET .../sync).
type SyncQuery struct {
	Cursor   string
	Wait     time.Duration
	ClientID string
	Subs     map[string]bool
}

// ParseSyncQuery reads cursor, wait (seconds), client_id and subscribe.
func ParseSyncQuery(get func(string) string) SyncQuery {
	q := SyncQuery{Cursor: get("cursor"), ClientID: get("client_id"), Subs: map[string]bool{}}
	if w, err := strconv.ParseFloat(get("wait"), 64); err == nil && w > 0 {
		q.Wait = time.Duration(w * float64(time.Second))
	}
	if q.Wait > contract.LongPollMaxHold {
		q.Wait = contract.LongPollMaxHold
	}
	for _, r := range strings.Split(get("subscribe"), ",") {
		if r = strings.TrimSpace(r); r != "" {
			q.Subs[r] = true
		}
	}
	return q
}

func welcome(p contract.Participant, cursor string) contract.Message {
	return contract.Message{Type: contract.MsgWelcome, Payload: &contract.WelcomePayload{Contract: contract.ContractVersion,
		Participant: p, ServerTime: time.Now().UTC(), Cursor: cursor}}
}

// Sync is the long-poll fallback shared by the loopback route and the
// Kandev `sync` webhook (protocol §6).
func Sync(ctx context.Context, svc *app.Service, mapID string, p contract.Participant, q SyncQuery) (contract.SyncResponse, error) {
	if q.Cursor == "" {
		snap, cursor, err := svc.SnapshotWithCursor(mapID)
		if err != nil {
			return contract.SyncResponse{}, err
		}
		return contract.SyncResponse{Cursor: cursor, Messages: []contract.Message{
			welcome(p, cursor), {Type: contract.MsgSnapshot, Payload: &snap},
		}}, nil
	}
	if _, err := svc.Store.GetMap(mapID); err != nil {
		return contract.SyncResponse{}, err
	}
	res := svc.Hub.Wait(ctx, mapID, q.Cursor, q.Wait, q.ClientID, q.Subs)
	if !res.Behind {
		return contract.SyncResponse{Cursor: res.Cursor, Messages: res.Messages}, nil
	}
	return catchUp(svc, mapID, res.FromRev)
}

// catchUp answers a cursor this process cannot serve from its ring: ops from
// the log when the gap is small, else a snapshot.
func catchUp(svc *app.Service, mapID string, fromRev int64) (contract.SyncResponse, error) {
	m, err := svc.Store.GetMap(mapID)
	if err != nil {
		return contract.SyncResponse{}, err
	}
	if fromRev > 0 && fromRev <= m.Rev && m.Rev-fromRev <= maxCatchUpOps {
		cursor := svc.Hub.Cursor(mapID)
		ops, err := svc.Store.OpsSince(mapID, fromRev, 0)
		if err != nil {
			return contract.SyncResponse{}, err
		}
		msgs := []contract.Message{}
		if len(ops) > 0 {
			msgs = append(msgs, contract.Message{Type: contract.MsgOps, Payload: &contract.OpsPayload{Ops: ops, Rev: ops[len(ops)-1].Rev}})
		}
		return contract.SyncResponse{Cursor: cursor, Messages: msgs}, nil
	}
	snap, cursor, err := svc.SnapshotWithCursor(mapID)
	if err != nil {
		return contract.SyncResponse{}, err
	}
	return contract.SyncResponse{Cursor: cursor, Messages: []contract.Message{{Type: contract.MsgSnapshot, Payload: &snap}}}, nil
}

// SubmitOutcome turns a submit result into the ack/reject/rebase messages
// the WS would send: rebases first, then one ack for the unchanged ops.
func SubmitOutcome(ops []contract.Op, res store.SubmitResult, err error) []contract.Message {
	if err != nil {
		ids := make([]string, 0, len(ops))
		for _, o := range ops {
			ids = append(ids, o.ID)
		}
		return []contract.Message{{Type: contract.MsgReject, Payload: &contract.RejectPayload{IDs: ids, Error: *contract.AsError(err)}}}
	}
	var msgs []contract.Message
	rebased := map[string]bool{}
	for _, o := range res.Rebased {
		rebased[o.ID] = true
		msgs = append(msgs, contract.Message{Type: contract.MsgRebase, Payload: &contract.RebasePayload{Op: o}})
	}
	acks := []contract.OpAck{}
	for _, a := range res.Acks {
		if !rebased[a.ID] {
			acks = append(acks, a)
		}
	}
	if len(acks) > 0 {
		msgs = append(msgs, contract.Message{Type: contract.MsgAck, Payload: &contract.AckPayload{Acks: acks}})
	}
	return msgs
}

// Submit is POST .../ops and the `submit` webhook: presence and/or ops,
// answered with a SyncResponse holding this batch's ack/reject/rebase.
func Submit(svc *app.Service, mapID string, p contract.Participant, req contract.SubmitRequest) (contract.SyncResponse, error) {
	if _, err := svc.Store.GetMap(mapID); err != nil {
		return contract.SyncResponse{}, err
	}
	if req.Presence != nil {
		pr := *req.Presence
		pr.ParticipantID = p.ID
		svc.Hub.UpdatePresence(mapID, pr)
	}
	out := contract.SyncResponse{Messages: []contract.Message{}}
	if len(req.Ops) > 0 {
		res, err := svc.Submit(mapID, req.Ops, app.SubmitOptions{Actor: contract.Actor{ParticipantID: p.ID}, OriginClient: req.ClientID})
		if e := contract.AsError(err); e != nil && e.Code == contract.ErrInternal {
			return out, err
		}
		out.Messages = SubmitOutcome(req.Ops, res, err)
	}
	// Cursor stays empty: the submit answer must not advance the client's
	// sync cursor past ops it has not received yet.
	return out, nil
}
