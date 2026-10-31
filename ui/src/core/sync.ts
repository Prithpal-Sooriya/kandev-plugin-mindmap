// The client side of docs/protocol.md §5 "Ordering and client algorithm".
//
//   view = confirmed (server state at rev) + early (acked, waiting for a rev gap) + pending (unacked, send order)
//
// The engine is transport-agnostic: the connection feeds it ServerMessages and
// gives it a `submit` function. Everything here is synchronous and pure enough
// to unit-test against the fake server.
import {
  newId, type ActivityEvent, type ContractErrorBody, type Op, type OutputLine, type Participant, type Presence,
  type Proposal, type Run, type ServerMessage,
} from "../contract";
import { applyOp, cloneDoc, docFromSnapshot, emptyDoc, type Doc } from "./doc";

/** An op as the UI creates it, before ids and actor are filled in. */
export type OpDraft = { [T in Op["type"]]: { type: T; payload: Extract<Op, { type: T }>["payload"] } }[Op["type"]];

export interface TrailMark {
  participantId: string;
  at: number;
}

export type EngineListener = () => void;

export class SyncEngine {
  confirmed: Doc = emptyDoc();
  pending: Op[] = [];
  /** Ops with a known rev that cannot be applied yet because an earlier rev is missing. */
  private early = new Map<number, Op>();
  hasSnapshot = false;
  participant: Participant | null = null;
  /** Participant to resume on the first hello (a guest's saved id; protocol §13). */
  resumeParticipantId: string | null = null;
  cursor = "";

  runs = new Map<string, Run>();
  proposals = new Map<string, Proposal>();
  participants = new Map<string, Participant>();
  presence = new Map<string, Presence>();
  activity: ActivityEvent[] = [];
  output = new Map<string, OutputLine[]>();
  /** Nodes recently written by others (write trail), and agent-created nodes not yet seen. */
  trail = new Map<string, TrailMark>();
  unseen = new Set<string>();

  /** Bumps on every change; components compare it to decide whether to re-render. */
  version = 0;
  docVersion = 0;
  private viewCache: Doc | null = null;
  private listeners = new Set<EngineListener>();

  /** Set by the connection. Called with each local batch, and with the whole queue on reconnect. */
  submit: (ops: Op[]) => void = () => {};
  /** Called when the engine needs a fresh snapshot (unknown node in a restore, or a persistent rev gap). */
  onResync: () => void = () => {};
  /** Called with errors the user should see (rejects, fatal errors). */
  onError: (e: ContractErrorBody, ops?: Op[]) => void = () => {};

  constructor(readonly mapId: string, private now: () => number = Date.now) {}

  subscribe(fn: EngineListener): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  private changed(doc: boolean): void {
    this.version++;
    if (doc) {
      this.docVersion++;
      this.viewCache = null;
    }
    for (const fn of this.listeners) fn();
  }

  get rev(): number {
    return this.confirmed.rev;
  }

  /** confirmed + early + pending, memoised until the next change. */
  get view(): Doc {
    if (this.viewCache) return this.viewCache;
    if (this.early.size === 0 && this.pending.length === 0) return (this.viewCache = this.confirmed);
    const v = cloneDoc(this.confirmed);
    for (const r of [...this.early.keys()].sort((a, b) => a - b)) applyOp(v, this.early.get(r)!);
    for (const op of this.pending) applyOp(v, op);
    return (this.viewCache = v);
  }

  /** hello payload fields that depend on engine state. */
  resumeFields(): { participant_id?: string; since_rev?: number } {
    return {
      ...(this.participant?.id || this.resumeParticipantId ? { participant_id: this.participant?.id || this.resumeParticipantId! } : {}),
      ...(this.hasSnapshot && this.confirmed.rev > 0 ? { since_rev: this.confirmed.rev } : {}),
    };
  }

  /** Optimistically applies a batch of local edits and submits it. Returns the ops. */
  local(drafts: OpDraft[]): Op[] {
    if (!drafts.length) return [];
    const actor = { participant_id: this.participant?.id ?? "pt_local0000000000" };
    const batch = drafts.length > 1 ? newId("bt") : undefined;
    const ops = drafts.map(
      (d) =>
        ({
          id: newId("op"), map_id: this.mapId, rev: 0, type: d.type, payload: d.payload, actor,
          base_rev: this.confirmed.rev || undefined, ...(batch ? { batch_id: batch } : {}),
        }) as Op,
    );
    for (const o of ops) if (o.base_rev === undefined) delete o.base_rev;
    this.pending.push(...ops);
    this.changed(true);
    this.submit(ops);
    return ops;
  }

  /** Every unacked op, for re-submission after reconnect. Ids make this idempotent. */
  resendQueue(): Op[] {
    return [...this.pending];
  }

  handle(msg: ServerMessage): void {
    switch (msg.type) {
      case "welcome":
        this.participant = msg.payload.participant;
        this.participants.set(msg.payload.participant.id, msg.payload.participant);
        if (msg.payload.cursor) this.cursor = msg.payload.cursor;
        return this.changed(false);
      case "snapshot": {
        const s = msg.payload;
        this.confirmed = docFromSnapshot(s);
        this.early.clear();
        this.runs = new Map(s.runs.map((r) => [r.id, r]));
        this.proposals = new Map(s.proposals.map((p) => [p.id, p]));
        this.participants = new Map(s.participants.map((p) => [p.id, p]));
        if (this.participant) this.participants.set(this.participant.id, this.participant);
        this.presence = new Map(s.presence.map((p) => [p.participant_id, p]));
        this.activity = s.activity.slice(-200);
        this.hasSnapshot = true;
        return this.changed(true);
      }
      case "ops":
        for (const op of msg.payload.ops) this.commit(op, true);
        this.drain();
        return this.changed(true);
      case "ack": {
        for (const a of msg.payload.acks) {
          const i = this.pending.findIndex((o) => o.id === a.id);
          if (i < 0) continue; // duplicate ack after a resubmit, or already seen in catch-up ops
          const [op] = this.pending.splice(i, 1);
          this.commit({ ...op!, rev: a.rev }, false);
        }
        this.drain();
        return this.changed(true);
      }
      case "rebase": {
        const op = msg.payload.op;
        const i = this.pending.findIndex((o) => o.id === op.id);
        if (i >= 0) this.pending.splice(i, 1);
        if (op.rev > 0) {
          this.commit(op, false);
          this.drain();
        } else if (i >= 0) this.pending.splice(i, 0, op);
        return this.changed(true);
      }
      case "reject": {
        const ids = new Set(msg.payload.ids);
        const dropped = this.pending.filter((o) => ids.has(o.id));
        this.pending = this.pending.filter((o) => !ids.has(o.id));
        this.onError(msg.payload.error, dropped);
        return this.changed(true);
      }
      case "presence":
        for (const p of msg.payload.presence ?? []) {
          if (p.participant_id !== this.participant?.id) this.presence.set(p.participant_id, p);
        }
        for (const id of msg.payload.left ?? []) this.presence.delete(id);
        return this.changed(false);
      case "participant":
        this.participants.set(msg.payload.participant.id, msg.payload.participant);
        return this.changed(false);
      case "run":
        this.runs.set(msg.payload.run.id, msg.payload.run);
        return this.changed(false);
      case "proposal":
        this.proposals.set(msg.payload.proposal.id, msg.payload.proposal);
        return this.changed(true);
      case "activity": {
        const seen = new Set(this.activity.map((e) => e.id));
        this.activity = [...this.activity, ...msg.payload.events.filter((e) => !seen.has(e.id))].slice(-200);
        return this.changed(false);
      }
      case "output": {
        const lines = [...(this.output.get(msg.payload.run_id) ?? []), ...msg.payload.lines].slice(-500);
        this.output.set(msg.payload.run_id, lines);
        return this.changed(false);
      }
      case "error":
        if (msg.payload.error.code === "resync") {
          this.hasSnapshot = false;
          this.onResync();
        }
        this.onError(msg.payload.error);
        return;
      case "pong":
        return;
    }
  }

  /** Queues a committed op by rev. Ops from others may also be our own ops seen in catch-up. */
  private commit(op: Op, fromOthers: boolean): void {
    if (op.rev <= this.confirmed.rev) return; // already applied (duplicate ack or overlapping catch-up)
    if (fromOthers) {
      const i = this.pending.findIndex((o) => o.id === op.id);
      if (i >= 0) this.pending.splice(i, 1); // our own op, committed before a reconnect: treat as its ack
      else this.markTrail(op);
    }
    this.early.set(op.rev, op);
  }

  private drain(): void {
    let next = this.confirmed.rev + 1;
    while (this.early.has(next)) {
      const op = this.early.get(next)!;
      this.early.delete(next);
      if (op.type === "node.restore" && !this.confirmed.nodes.has(op.payload.id)) {
        // Snapshots omit tombstones, so we may not know the restored subtree.
        this.confirmed.rev = next;
        this.hasSnapshot = false;
        this.onResync();
        return;
      }
      applyOp(this.confirmed, op);
      this.confirmed.rev = next;
      if (this.confirmed.map) this.confirmed.map = { ...this.confirmed.map, rev: next };
      next++;
    }
    this.gapSince = this.early.size ? (this.gapSince ?? this.now()) : null;
  }

  private gapSince: number | null = null;

  /** True when committed ops have waited for a missing rev for longer than ms: ask for a snapshot. */
  stalled(ms: number): boolean {
    return this.gapSince !== null && this.now() - this.gapSince > ms;
  }

  private markTrail(op: Op): void {
    if (op.actor.participant_id === this.participant?.id) return;
    const at = this.now();
    const mark = (id: string): void => {
      this.trail.set(id, { participantId: op.actor.participant_id, at });
    };
    switch (op.type) {
      case "node.create":
        mark(op.payload.node.id);
        if (op.actor.run_id) this.unseen.add(op.payload.node.id);
        return;
      case "node.update":
      case "node.move":
      case "node.claim":
        return mark(op.payload.id);
      case "node.merge":
        return mark(op.payload.into_id);
      case "asset.attach":
        return mark(op.payload.asset.node_id);
    }
  }

  /** Drops trail marks older than ttl ms. Returns true if anything expired. */
  expireTrail(ttl: number): boolean {
    const cutoff = this.now() - ttl;
    let any = false;
    for (const [id, m] of this.trail) if (m.at < cutoff) (this.trail.delete(id), (any = true));
    if (any) this.changed(false);
    return any;
  }

  markSeen(id: string): void {
    if (this.unseen.delete(id)) this.changed(false);
  }

  /** Replaces a known proposal (after a review call returned the new state). */
  setProposal(p: Proposal): void {
    this.proposals.set(p.id, p);
    this.changed(true);
  }
}
