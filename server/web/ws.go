package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"

	"kandev-plugin-mindmap/server/app"
	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/hub"
	"kandev-plugin-mindmap/server/store"
)

const (
	helloTimeout = 10 * time.Second
	idleTimeout  = 60 * time.Second // no frame from the client (they ping every 20 s)
	writeTimeout = 10 * time.Second
	maxFrame     = 4 << 20
)

func (s *Server) upgrader(p *principal) *websocket.Upgrader {
	return &websocket.Upgrader{
		ReadBufferSize: 16 << 10, WriteBufferSize: 32 << 10,
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if p.kind == contract.TokenShare {
				if origin == "" {
					return true
				}
				u, err := url.Parse(origin)
				return err == nil && u.Host == r.Host
			}
			return s.svc.OriginAllowed(origin)
		},
	}
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request, p *principal) {
	mapID := r.URL.Query().Get("map_id")
	if mapID == "" {
		mapID = p.scope
	}
	if mapID == "" || !p.canAccess(mapID) {
		if p.kind == contract.TokenShare {
			notFound(w)
		} else {
			writeErr(w, contract.FieldErr(contract.ErrBadRequest, "map_id", "required for an all-maps token"))
		}
		return
	}
	if _, err := s.svc.Store.GetMap(mapID); err != nil {
		if p.kind == contract.TokenShare {
			notFound(w)
		} else {
			writeErr(w, err)
		}
		return
	}
	conn, err := s.upgrader(p).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(maxFrame)
	go s.serveConn(conn, p, mapID)
}

func writeFrame(conn *websocket.Conn, m contract.Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return conn.WriteMessage(websocket.TextMessage, b)
}

func fatal(conn *websocket.Conn, e *contract.Error) {
	_ = writeFrame(conn, contract.Message{Type: contract.MsgError, Payload: &contract.ErrorPayload{Error: *e, Fatal: true}})
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, string(e.Code)), time.Now().Add(time.Second))
	_ = conn.Close()
}

func readMsg(conn *websocket.Conn) (contract.Message, error) {
	_, b, err := conn.ReadMessage()
	if err != nil {
		return contract.Message{}, err
	}
	var m contract.Message
	if err := json.Unmarshal(b, &m); err != nil {
		return contract.Message{}, contract.AsError(err)
	}
	return m, nil
}

// helloParticipant picks who a connection acts as: the host for local
// tokens; for share tokens a resumed or new guest participant.
func (s *Server) helloParticipant(p *principal, mapID string, h *contract.HelloPayload) (contract.Participant, error) {
	if p.kind == contract.TokenLocal {
		return s.svc.Store.HostParticipant(mapID)
	}
	if h.ParticipantID != "" {
		if pt, err := s.svc.Store.GetParticipant(h.ParticipantID); err == nil && pt.MapID == mapID && pt.Kind == contract.ParticipantHuman && !pt.IsHost {
			return pt, nil
		}
	}
	pt, err := s.svc.Store.CreateGuest(mapID, h.Name, h.Color)
	if err == nil {
		s.svc.Activity(contract.ActivityEvent{MapID: mapID, Type: contract.ActivityParticipantJoined, Actor: contract.Actor{ParticipantID: pt.ID},
			Summary: pt.Name + " joined"})
	}
	return pt, err
}

func (s *Server) serveConn(conn *websocket.Conn, p *principal, mapID string) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(helloTimeout))
	first, err := readMsg(conn)
	if err != nil {
		fatal(conn, contract.Errf(contract.ErrBadRequest, "first frame must be hello: %v", err))
		return
	}
	hello, ok := first.Payload.(*contract.HelloPayload)
	if !ok || first.Type != contract.MsgHello {
		fatal(conn, contract.Errf(contract.ErrBadRequest, "first frame must be hello"))
		return
	}
	if hello.Contract != contract.ContractVersion {
		fatal(conn, contract.Errf(contract.ErrVersion, "server speaks contract %d", contract.ContractVersion))
		return
	}
	pt, err := s.helloParticipant(p, mapID, hello)
	if err != nil {
		fatal(conn, contract.AsError(err))
		return
	}
	_ = s.svc.Store.TouchParticipant(pt.ID)
	pt.Online = true
	c := hub.NewClient(store.NewID("cn"), hello.ClientID, mapID, pt)
	if err := s.svc.Join(c, hello.SinceRev, maxCatchUpOps); err != nil {
		fatal(conn, contract.AsError(err))
		return
	}
	s.svc.Hub.Publish(mapID, contract.Message{Type: contract.MsgParticipant, Payload: &contract.ParticipantPayload{Participant: pt}})
	done := make(chan struct{})
	go s.writeLoop(conn, c, done)
	defer func() {
		close(done)
		s.svc.Hub.Leave(c)
		if !s.svc.Hub.Online(mapID)[pt.ID] {
			pt.Online = false
			s.svc.Hub.Publish(mapID, contract.Message{Type: contract.MsgParticipant, Payload: &contract.ParticipantPayload{Participant: pt}})
		}
	}()
	actor := contract.Actor{ParticipantID: pt.ID}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(idleTimeout))
		m, err := readMsg(conn)
		if err != nil {
			if ce, ok := err.(*contract.Error); ok {
				c.Send(contract.Message{Type: contract.MsgError, Payload: &contract.ErrorPayload{Error: *ce}})
				continue
			}
			return
		}
		switch pl := m.Payload.(type) {
		case *contract.SubmitPayload:
			res, err := s.svc.Submit(mapID, pl.Ops, app.SubmitOptions{Actor: actor, OriginConn: c.ConnID, OriginClient: c.ClientID})
			for _, out := range SubmitOutcome(pl.Ops, res, err) {
				c.Send(out)
			}
		case *contract.PresenceUpdatePayload:
			pr := pl.Presence
			pr.ParticipantID = pt.ID
			s.svc.Hub.UpdatePresence(mapID, pr)
		case *contract.OutputSubscriptionPayload:
			c.Subscribe(pl.RunID, m.Type == contract.MsgOutputSubscribe)
		case *contract.PingPayload:
			c.Send(contract.Message{Type: contract.MsgPong, Payload: &contract.PingPayload{Nonce: pl.Nonce}})
		case *contract.HelloPayload:
			c.Send(contract.Message{Type: contract.MsgError, Payload: &contract.ErrorPayload{Error: *contract.Errf(contract.ErrBadRequest, "already said hello")}})
		default:
			c.Send(contract.Message{Type: contract.MsgError, Payload: &contract.ErrorPayload{Error: *contract.Errf(contract.ErrBadRequest, "%s is a server message", m.Type)}})
		}
	}
}

func (s *Server) writeLoop(conn *websocket.Conn, c *hub.Client, done <-chan struct{}) {
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-done:
			return
		case <-c.Dropped():
			fatal(conn, contract.Errf(contract.ErrResync, "connection too slow; reconnect and send hello again"))
			return
		case m := <-c.Out:
			if err := writeFrame(conn, m); err != nil {
				_ = conn.Close()
				return
			}
		case <-ping.C:
			_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout))
		}
	}
}
