// An in-memory implementation of the server side of docs/protocol.md, used by
// the vitest suite and the standalone dev server while the Go backend is built
// in parallel. It follows the protocol, not the Go code: single writer per
// map, gapless revs, atomic batches, idempotent op ids, order-key collision
// rebases, cycle/deleted/locked rejects, proposals, and long-poll cursors.
import {
  CONTRACT_VERSION, buildParentIndex, checkMove, compareSiblings, newId, orderBetween, validOrderKey,
  type ActivityEvent, type ClientMessage, type ContractErrorBody, type ErrorCode, type MapNode, type MapSummary,
  type MindMap, type Op, type Participant, type Presence, type Proposal, type Run, type ServerMessage, type Snapshot,
  type SubmitRequest, type SyncResponse,
} from "../contract";
import { applyOp, cloneDoc, emptyDoc, type Doc } from "../core/doc";
import type { OpDraft } from "../core/sync";
import { inverseOf } from "../core/undo";

export interface FakeConn {
  readonly id: string;
  mapId: string;
  participantId: string;
  clientId: string;
  greeted: boolean;
  send(msg: ServerMessage): void;
}

interface Buffered {
  seq: number;
  msg: ServerMessage;
  /** Participant that caused it and should not get it back (presence echo). */
  from?: string;
}

interface Batch {
  participantId: string;
  inverse: OpDraft[];
  undone: boolean;
}

interface MapState {
  doc: Doc;
  log: Op[];
  opRevs: Map<string, number>;
  /** op id -> client_id that submitted it (long-poll echo suppression, §13). */
  opClient: Map<string, string>;
  /** Committed batches with their inverses, for per-author undo (§13). */
  batches: Batch[];
  runs: Map<string, Run>;
  proposals: Map<string, Proposal>;
  participants: Map<string, Participant>;
  presence: Map<string, Presence>;
  activity: ActivityEvent[];
  buffer: Buffered[];
  seq: number;
  waiters: Set<() => void>;
}

const COLORS = ["#ef4444", "#3b82f6", "#22c55e", "#a855f7", "#f59e0b", "#06b6d4", "#ec4899", "#84cc16"];

class Reject extends Error {
  constructor(readonly code: ErrorCode, msg: string, readonly field?: string) {
    super(msg);
  }
}

export class FakeServer {
  readonly maps = new Map<string, MapState>();
  private conns = new Set<FakeConn>();
  private connSeq = 0;
  /** Test hook: called before each submit is processed; may throw a Reject-like error code. */
  failNextSubmit: ErrorCode | null = null;

  constructor(private clock: () => number = Date.now) {}

  now(): string {
    return new Date(this.clock()).toISOString().replace(/\.\d+Z$/, "Z");
  }

  // --- maps -----------------------------------------------------------------

  createMap(title: string, workspaceId = "ws-local", host?: Participant): MindMap {
    const id = newId("map");
    const st: MapState = {
      doc: emptyDoc(), log: [], opRevs: new Map(), opClient: new Map(), batches: [], runs: new Map(), proposals: new Map(), participants: new Map(),
      presence: new Map(), activity: [], buffer: [], seq: 0, waiters: new Set(),
    };
    const hostP = host
      ? { ...host, map_id: id }
      : this.makeParticipant(id, { name: "Host", kind: "human", is_host: true, color: COLORS[0] });
    st.participants.set(hostP.id, hostP);
    const rootId = newId("nd");
    const ts = this.now();
    st.doc.map = {
      id, workspace_id: workspaceId, title, root_id: rootId, settings: {}, rev: 0, created_by: hostP.id,
      created_at: ts, updated_at: ts,
    };
    this.maps.set(id, st);
    this.commitAs(id, hostP.id, [
      { type: "node.create", payload: { node: { id: rootId, order: "V", title, kind: "topic" } } },
    ], {}, false);
    return st.doc.map!;
  }

  listMaps(): MapSummary[] {
    return [...this.maps.values()].map((s) => ({
      id: s.doc.map!.id, title: s.doc.map!.title, workspace_id: s.doc.map!.workspace_id,
      nodes: [...s.doc.nodes.values()].filter((n) => !n.deleted).length,
      active_runs: [...s.runs.values()].filter((r) => r.status === "running" || r.status === "queued").length,
      updated_at: s.doc.map!.updated_at,
    }));
  }

  state(mapId: string): MapState {
    const s = this.maps.get(mapId);
    if (!s) throw new Reject("not_found", "no such map");
    return s;
  }

  snapshot(mapId: string): Snapshot {
    const s = this.state(mapId);
    return {
      rev: s.doc.rev, map: { ...s.doc.map!, rev: s.doc.rev },
      nodes: [...s.doc.nodes.values()].filter((n) => !n.deleted),
      links: [...s.doc.links.values()].filter((l) => !l.deleted),
      runs: [...s.runs.values()], proposals: [...s.proposals.values()], participants: [...s.participants.values()],
      presence: [...s.presence.values()], activity: s.activity.slice(-50),
    };
  }

  makeParticipant(mapId: string, p: Partial<Participant>): Participant {
    const ts = this.now();
    const st = this.maps.get(mapId);
    const n = st ? st.participants.size : 0;
    const out: Participant = {
      id: p.id ?? newId("pt"), map_id: mapId, kind: p.kind ?? "human", name: p.name ?? `Guest ${n + 1}`,
      color: p.color ?? COLORS[n % COLORS.length]!, joined_at: ts, last_seen_at: ts, online: true,
      ...(p.is_host ? { is_host: true } : {}), ...(p.harness ? { harness: p.harness } : {}),
      ...(p.run_id ? { run_id: p.run_id } : {}),
    };
    st?.participants.set(out.id, out);
    return out;
  }

  addParticipant(mapId: string, p: Partial<Participant>): Participant {
    const out = this.makeParticipant(mapId, p);
    this.broadcast(mapId, { type: "participant", payload: { participant: out } });
    return out;
  }

  hostOf(mapId: string): Participant {
    return [...this.state(mapId).participants.values()].find((p) => p.is_host)!;
  }

  // --- WebSocket ------------------------------------------------------------

  connect(mapId: string, send: (m: ServerMessage) => void): FakeConn {
    const c: FakeConn = { id: `c${++this.connSeq}`, mapId, participantId: "", clientId: "", greeted: false, send };
    this.conns.add(c);
    return c;
  }

  disconnect(c: FakeConn): void {
    this.conns.delete(c);
    const st = this.maps.get(c.mapId);
    if (st && c.participantId && ![...this.conns].some((o) => o.participantId === c.participantId)) {
      st.presence.delete(c.participantId);
      this.broadcast(c.mapId, { type: "presence", payload: { left: [c.participantId] } }, c.participantId);
    }
  }

  receive(c: FakeConn, msg: ClientMessage): void {
    if (!c.greeted && msg.type !== "hello") {
      c.send({ type: "error", payload: { error: { code: "bad_request", message: "hello must be first" }, fatal: true } });
      return;
    }
    switch (msg.type) {
      case "hello": {
        const st = this.state(c.mapId);
        if (msg.payload.contract !== CONTRACT_VERSION) {
          c.send({ type: "error", payload: { error: { code: "version", message: "contract version" }, fatal: true } });
          return;
        }
        let p = msg.payload.participant_id ? st.participants.get(msg.payload.participant_id) : undefined;
        if (!p) {
          p = msg.payload.name
            ? this.addParticipant(c.mapId, { name: msg.payload.name, color: msg.payload.color })
            : this.hostOf(c.mapId);
        }
        c.participantId = p.id;
        c.clientId = msg.payload.client_id;
        c.greeted = true;
        c.send({ type: "welcome", payload: { contract: CONTRACT_VERSION, participant: p, server_time: this.now(), cursor: this.cursor(st) } });
        const since = msg.payload.since_rev;
        if (since !== undefined && since <= st.doc.rev) {
          const ops = st.log.filter((o) => o.rev > since);
          if (ops.length) c.send({ type: "ops", payload: { ops, rev: st.doc.rev } });
        } else c.send({ type: "snapshot", payload: this.snapshot(c.mapId) });
        return;
      }
      case "submit": {
        const out = this.submit(c.mapId, c.participantId, msg.payload.ops, c);
        for (const m of out) c.send(m);
        return;
      }
      case "presence.update":
        return this.setPresence(c.mapId, { ...msg.payload.presence, participant_id: c.participantId });
      case "ping":
        return c.send({ type: "pong", payload: msg.payload });
      case "output.subscribe":
      case "output.unsubscribe":
        return;
    }
  }

  // --- ops ------------------------------------------------------------------

  /**
   * Validates and commits one batch. Returns the frames for the submitter
   * (rebases, then ack; or one reject). Other connections get `ops` first.
   */
  submit(mapId: string, participantId: string, ops: Op[], origin?: FakeConn, clientId = origin?.clientId): ServerMessage[] {
    const st = this.state(mapId);
    const acks: { id: string; rev: number }[] = [];
    const fresh = ops.filter((o) => {
      const rev = st.opRevs.get(o.id);
      if (rev !== undefined) acks.push({ id: o.id, rev });
      return rev === undefined;
    });
    if (this.failNextSubmit) {
      const code = this.failNextSubmit;
      this.failNextSubmit = null;
      return [{ type: "reject", payload: { ids: fresh.map((o) => o.id), error: { code, message: "injected failure" } } }];
    }
    if (!fresh.length) return acks.length ? [{ type: "ack", payload: { acks } }] : [];
    let committed: { op: Op; rebased: boolean }[];
    try {
      // The actor comes from the token, never from the client.
      committed = this.apply(st, participantId, fresh.map((o) => ({ ...o, actor: { participant_id: participantId } })));
    } catch (e) {
      const error: ContractErrorBody = e instanceof Reject
        ? { code: e.code, message: e.message, ...(e.field ? { field: e.field } : {}) }
        : { code: "internal", message: String(e) };
      return [{ type: "reject", payload: { ids: fresh.map((o) => o.id), error } }];
    }
    if (clientId) for (const c of committed) st.opClient.set(c.op.id, clientId);
    this.broadcastOps(mapId, committed.map((c) => c.op), origin);
    const out: ServerMessage[] = [];
    for (const c of committed) {
      if (c.rebased) out.push({ type: "rebase", payload: { op: c.op } });
      else acks.push({ id: c.op.id, rev: c.op.rev });
    }
    if (acks.length) out.push({ type: "ack", payload: { acks } });
    return out;
  }

  /** Commits ops as a participant that has no connection (agents, the dev bot). Broadcasts to everyone. */
  commitAs(mapId: string, participantId: string, drafts: OpDraft[], extra: Partial<Op> = {}, undoable = true): Op[] {
    const st = this.state(mapId);
    const p = st.participants.get(participantId);
    const ops = drafts.map((d) => ({
      id: newId("op"), map_id: mapId, rev: 0, type: d.type, payload: d.payload,
      actor: { participant_id: participantId, ...(p?.run_id ? { run_id: p.run_id } : {}) }, ...extra,
    }) as Op);
    const committed = this.apply(st, participantId, ops, undoable).map((c) => c.op);
    this.broadcastOps(mapId, committed);
    return committed;
  }

  /**
   * POST .../undo (§13): reverts the caller's latest batch that is not undone
   * yet, as a new batch broadcast to everyone (the caller included).
   */
  undo(mapId: string, participantId: string): { ops: Op[]; rev: number } {
    const st = this.state(mapId);
    const b = [...st.batches].reverse().find((x) => x.participantId === participantId && !x.undone);
    if (!b) throw new Reject("conflict", "nothing to undo");
    if (!b.inverse.length) throw new Reject("conflict", "that edit cannot be undone");
    const ops = this.commitAs(mapId, participantId, b.inverse, {}, false);
    b.undone = true;
    return { ops, rev: st.doc.rev };
  }

  private apply(st: MapState, participantId: string, ops: Op[], undoable = true): { op: Op; rebased: boolean }[] {
    const work = cloneDoc(st.doc);
    const out: { op: Op; rebased: boolean }[] = [];
    let rev = st.doc.rev;
    const ts = this.now();
    const inverse: OpDraft[] = [];
    for (const original of ops) {
      const { op, rebased } = this.check(st, work, participantId, original);
      const done = { ...op, rev: ++rev, ts } as Op;
      delete (done as { base_rev?: number }).base_rev;
      if (undoable) inverse.unshift(...inverseOf(work, [{ type: done.type, payload: done.payload } as OpDraft]));
      applyOp(work, done);
      work.rev = rev;
      out.push({ op: done, rebased });
    }
    st.doc = work;
    if (st.doc.map) st.doc.map = { ...st.doc.map, rev, updated_at: ts };
    for (const { op } of out) {
      st.log.push(op);
      st.opRevs.set(op.id, op.rev);
    }
    if (undoable) st.batches.push({ participantId, inverse, undone: false });
    this.wake(st);
    return out;
  }

  private check(st: MapState, d: Doc, who: string, op: Op): { op: Op; rebased: boolean } {
    const live = (id: string, field: string): MapNode => {
      const n = d.nodes.get(id);
      if (!n) throw new Reject("not_found", `no node ${id}`, field);
      if (n.deleted) throw new Reject("deleted", `node ${id} is deleted`, field);
      return n;
    };
    const rootId = d.map?.root_id ?? "";
    switch (op.type) {
      case "node.create": {
        const n = op.payload.node;
        if (d.nodes.has(n.id)) throw new Reject("conflict", "duplicate node id", "node.id");
        if (!n.title.trim() || /[\r\n]/.test(n.title)) throw new Reject("invalid_op", "title must be one non-empty line", "node.title");
        if (!validOrderKey(n.order)) throw new Reject("invalid_op", "bad order key", "node.order");
        if (!n.parent_id) {
          if (d.nodes.size) throw new Reject("invalid_op", "parent_id required", "node.parent_id");
          return { op, rebased: false };
        }
        live(n.parent_id, "node.parent_id");
        const order = this.freeOrder(d, n.parent_id, n.order, n.id);
        if (order === n.order) return { op, rebased: false };
        return { op: { ...op, payload: { node: { ...n, order } } }, rebased: true };
      }
      case "node.update": {
        live(op.payload.id, "id");
        const set = op.payload.set;
        if (set?.title !== undefined && (!set.title.trim() || /[\r\n]/.test(set.title))) {
          throw new Reject("invalid_op", "title must be one non-empty line", "set.title");
        }
        for (const field of ["title", "body_md"] as const) {
          if (set?.[field] === undefined) continue;
          for (const p of st.presence.values()) {
            if (p.participant_id === who || !p.editing) continue;
            if (p.editing.node_id === op.payload.id && p.editing.field === field && Date.parse(p.editing.expires_at) > this.clock()) {
              throw new Reject("locked", `${field} is being edited by someone else`, `set.${field}`);
            }
          }
        }
        return { op, rebased: false };
      }
      case "node.move": {
        const p = op.payload;
        live(p.id, "id");
        live(p.parent_id, "parent_id");
        const err = checkMove(buildParentIndex(d.nodes.values()), rootId, p.id, p.parent_id);
        if (err) throw new Reject(err, err === "cycle" ? "can't move a node into its own branch" : err, "parent_id");
        const order = this.freeOrder(d, p.parent_id, p.order, p.id);
        return order === p.order ? { op, rebased: false } : { op: { ...op, payload: { ...p, order } }, rebased: true };
      }
      case "node.delete":
        if (op.payload.id === rootId) throw new Reject("root_immutable", "the central topic can't be deleted", "id");
        live(op.payload.id, "id");
        return { op, rebased: false };
      case "node.restore":
        if (!d.nodes.has(op.payload.id)) throw new Reject("not_found", "no such node", "id");
        return { op, rebased: false };
      case "link.create":
        live(op.payload.link.from, "link.from");
        live(op.payload.link.to, "link.to");
        return { op, rebased: false };
      default:
        return { op, rebased: false };
    }
  }

  /** Order-key collision rule (§7): keep the first, move the second between it and its next sibling. */
  private freeOrder(d: Doc, parentId: string, order: string, self: string): string {
    const sibs = [...d.nodes.values()]
      .filter((n) => n.parent_id === parentId && !n.deleted && n.id !== self)
      .sort(compareSiblings);
    const i = sibs.findIndex((s) => s.order === order);
    if (i < 0) return order;
    let j = i + 1;
    while (j < sibs.length && sibs[j]!.order === order) j++;
    return orderBetween(order, sibs[j]?.order ?? "");
  }

  private broadcastOps(mapId: string, ops: Op[], except?: FakeConn): void {
    if (!ops.length) return;
    const st = this.state(mapId);
    for (const c of this.conns) {
      if (c === except || c.mapId !== mapId || !c.greeted) continue;
      c.send({ type: "ops", payload: { ops, rev: st.doc.rev } });
    }
  }

  // --- non-op messages ---------------------------------------------------------

  /** Sends to every connection of the map (except `from`) and buffers it for long-poll. */
  broadcast(mapId: string, msg: ServerMessage, from?: string): void {
    const st = this.state(mapId);
    st.buffer.push({ seq: ++st.seq, msg, from });
    if (st.buffer.length > 2000) st.buffer.shift();
    for (const c of this.conns) {
      if (c.mapId === mapId && c.greeted && c.participantId !== from) c.send(msg);
    }
    this.wake(st);
  }

  setPresence(mapId: string, p: Presence): void {
    const st = this.state(mapId);
    const full = { ...p, updated_at: this.now() };
    st.presence.set(p.participant_id, full);
    this.broadcast(mapId, { type: "presence", payload: { presence: [full] } }, p.participant_id);
  }

  setRun(run: Run): void {
    this.state(run.map_id).runs.set(run.id, run);
    this.broadcast(run.map_id, { type: "run", payload: { run } });
  }

  addActivity(mapId: string, e: Omit<ActivityEvent, "id" | "map_id" | "ts">): void {
    const ev: ActivityEvent = { ...e, id: newId("ev"), map_id: mapId, ts: this.now() };
    this.state(mapId).activity.push(ev);
    this.broadcast(mapId, { type: "activity", payload: { events: [ev] } });
  }

  // --- proposals --------------------------------------------------------------

  /** Records ops as a proposal (propose-mode run). Node ids are minted now so ghosts can reference each other. */
  propose(mapId: string, runId: string, participantId: string, drafts: OpDraft[], summary?: string): Proposal {
    const st = this.state(mapId);
    const id = newId("prp");
    const prop: Proposal = {
      id, map_id: mapId, run_id: runId, base_rev: st.doc.rev, status: "ready",
      author: { participant_id: participantId, run_id: runId }, created_at: this.now(),
      items: drafts.map((d) => {
        const op = {
          id: newId("op"), map_id: mapId, rev: 0, type: d.type, payload: d.payload,
          actor: { participant_id: participantId, run_id: runId }, proposal_id: id,
        } as Op;
        return { op, status: "pending" as const, node_ids: touched(op) };
      }),
      ...(summary ? { summary } : {}),
    };
    st.proposals.set(id, prop);
    this.broadcast(mapId, { type: "proposal", payload: { proposal: prop } });
    return prop;
  }

  review(proposalId: string, req: { accept?: string[]; reject?: string[]; accept_all?: boolean; reject_all?: boolean }, by: string): Proposal {
    const st = [...this.maps.values()].find((s) => s.proposals.has(proposalId));
    if (!st) throw new Reject("not_found", "no such proposal");
    const prop = st.proposals.get(proposalId)!;
    if (["accepted", "rejected", "partial"].includes(prop.status)) throw new Reject("proposal_closed", "already resolved");
    const accept = new Set(req.accept ?? []), reject = new Set(req.reject ?? []);
    const failedNodes = new Set<string>();
    const items = prop.items.map((it) => ({ ...it }));
    for (const it of items) {
      if (it.status !== "pending") continue;
      const yes = req.accept_all || accept.has(it.op.id);
      const no = req.reject_all || reject.has(it.op.id);
      if (no || !yes) {
        if (no) {
          it.status = "rejected";
          if (it.op.type === "node.create") failedNodes.add(it.op.payload.node.id);
        }
        continue;
      }
      const deps = touched(it.op).filter((id) => failedNodes.has(id));
      if (deps.length) {
        it.status = "conflict";
        it.error = { code: "conflict", message: "depends on a rejected item" };
        continue;
      }
      try {
        this.commitAs(prop.map_id, prop.author.participant_id, [{ type: it.op.type, payload: it.op.payload } as OpDraft], {
          proposal_id: prop.id, id: it.op.id,
        });
        it.status = "accepted";
      } catch (e) {
        it.status = "conflict";
        it.error = { code: e instanceof Reject ? e.code : "internal", message: String((e as Error).message) };
        if (it.op.type === "node.create") failedNodes.add(it.op.payload.node.id);
      }
    }
    const pending = items.some((i) => i.status === "pending");
    const acc = items.filter((i) => i.status === "accepted").length;
    const status: Proposal["status"] = pending ? prop.status : acc === items.length ? "accepted" : acc === 0 ? "rejected" : "partial";
    const next: Proposal = { ...prop, items, status, ...(pending ? {} : { resolved_by: by, resolved_at: this.now() }) };
    st.proposals.set(prop.id, next);
    this.broadcast(prop.map_id, { type: "proposal", payload: { proposal: next } });
    return next;
  }

  // --- long-poll ----------------------------------------------------------------

  cursor(st: MapState): string {
    return `c${st.doc.rev}.${st.seq}`;
  }

  /** GET sync (§6). Without a cursor: [welcome, snapshot]. Otherwise everything since the cursor, held up to waitMs. */
  async sync(mapId: string, participantId: string, cursor: string | undefined, waitMs: number, clientId?: string): Promise<SyncResponse> {
    const st = this.state(mapId);
    const p = st.participants.get(participantId) ?? this.hostOf(mapId);
    if (!cursor) {
      return {
        cursor: this.cursor(st),
        messages: [
          { type: "welcome", payload: { contract: CONTRACT_VERSION, participant: p, server_time: this.now(), cursor: this.cursor(st) } },
          { type: "snapshot", payload: this.snapshot(mapId) },
        ],
      };
    }
    const m = /^c(\d+)\.(\d+)$/.exec(cursor);
    if (!m) return { cursor: this.cursor(st), messages: [{ type: "snapshot", payload: this.snapshot(mapId) }] };
    const rev = Number(m[1]), seq = Number(m[2]);
    const collect = (): ServerMessage[] => {
      const out: ServerMessage[] = [];
      const ops = st.log.filter((o) => o.rev > rev && (!clientId || st.opClient.get(o.id) !== clientId));
      if (ops.length) out.push({ type: "ops", payload: { ops, rev: st.doc.rev } });
      for (const b of st.buffer) if (b.seq > seq && b.from !== p.id) out.push(b.msg);
      return out;
    };
    let msgs = collect();
    if (!msgs.length && waitMs > 0) {
      await new Promise<void>((resolve) => {
        const t = setTimeout(done, Math.min(waitMs, 20_000));
        function done() {
          clearTimeout(t);
          st.waiters.delete(done);
          resolve();
        }
        st.waiters.add(done);
      });
      msgs = collect();
    }
    return { cursor: this.cursor(st), messages: msgs };
  }

  /** POST submit / ops (§6): the batch's ack/reject/rebase as a SyncResponse. Its cursor is always "" (§13). */
  submitHttp(mapId: string, participantId: string, req: SubmitRequest): SyncResponse {
    if (req.presence) this.setPresence(mapId, { ...req.presence, participant_id: participantId });
    const messages = req.ops.length ? this.submit(mapId, participantId, req.ops, undefined, req.client_id) : [];
    return { cursor: "", messages };
  }

  private wake(st: MapState): void {
    for (const w of [...st.waiters]) w();
  }
}

function touched(op: Op): string[] {
  switch (op.type) {
    case "node.create":
      return [op.payload.node.id, ...(op.payload.node.parent_id ? [op.payload.node.parent_id] : [])];
    case "node.update":
    case "node.delete":
    case "node.restore":
      return [op.payload.id];
    case "node.move":
      return [op.payload.id, op.payload.parent_id];
    case "node.merge":
      return [op.payload.into_id, ...op.payload.ids];
    case "link.create":
      return [op.payload.link.from, op.payload.link.to];
    default:
      return [];
  }
}

export { Reject as FakeReject };
