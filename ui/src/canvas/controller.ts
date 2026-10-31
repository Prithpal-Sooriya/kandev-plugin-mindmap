// Non-React state for one open map: the sync engine and its connection,
// selection, inline editing, undo, the ghost layer, and the derived display.
// Components subscribe to `version` and re-render at most once per frame.
import {
  ContractError, LIMITS, buildParentIndex, compareSiblings, newId, orderBetween, type ContractErrorBody, type MapNode,
  type NodeKind, type Presence, type ReviewProposalRequest,
} from "../contract";
import { childrenOf, cloneDoc, type Doc } from "../core/doc";
import { SyncEngine, type OpDraft } from "../core/sync";
import { UndoStack, inverseOf } from "../core/undo";
import type { HostAdapter } from "../host/adapter";
import { TreeLayout } from "../layout/tree";
import { Connection, type ConnState } from "../transport/connection";
import { buildDisplay, type Display } from "./display";

export interface Editing {
  id: string;
  /** Initial text when editing started by typing a character (replaces the title). */
  initial?: string;
  /** The node was just created: Esc keeps it, as in XMind. */
  fresh?: boolean;
  /** The user has typed since editing started (else the editor selects all). */
  touched?: boolean;
}

const ERROR_TEXT: Partial<Record<string, string>> = {
  cycle: "Can't move a node into its own branch.",
  root_immutable: "The central topic can't be moved or retired.",
  deleted: "That node was retired by someone else.",
  locked: "Someone else is editing that field.",
  rate_limited: "Slow down a little: too many edits per second.",
  conflict: "That edit conflicted with another change.",
};

export class MapController {
  readonly engine: SyncEngine;
  readonly conn: Connection;
  readonly undoStack = new UndoStack();
  private layoutEngine = new TreeLayout();
  private listeners = new Set<() => void>();
  private displayCache: { key: string; display: Display } | null = null;

  selection: string | null = null;
  editing: Editing | null = null;
  draft: { id: string; title: string } | null = null;
  showGhosts = true;
  inspectorOpen = false;
  paletteOpen = false;
  /** Characters typed after ⌘K but before the palette input mounted. */
  paletteSeed = "";
  connState: ConnState = "connecting";
  version = 0;
  private lockTimer: ReturnType<typeof setInterval> | null = null;
  private trailTimer: ReturnType<typeof setInterval> | null = null;
  private presence: Omit<Presence, "participant_id" | "updated_at"> = {};
  /** Last layout time in ms (perf overlay and dev measurements). */
  lastLayoutMs = 0;

  constructor(
    readonly mapId: string,
    readonly host: HostAdapter,
    opts: { clientId?: string; name?: string; color?: string; strict?: boolean } = {},
  ) {
    this.engine = new SyncEngine(mapId);
    this.conn = new Connection(this.engine, host.endpoints, {
      clientId: opts.clientId ?? newId("op").slice(3, 15), ws: host.ws, name: opts.name, color: opts.color,
      strict: opts.strict,
    });
    this.conn.onState = (s) => {
      this.connState = s;
      this.bump();
    };
    this.engine.onError = (e, ops) => this.reportError(e, ops?.length ?? 0);
    this.engine.subscribe(() => this.bump());
    // Guests resume their participant across reloads (each new hello would otherwise mint one).
    const key = `mm-pid:${mapId}`;
    if (host.kind === "standalone") {
      try {
        this.engine.resumeParticipantId = localStorage.getItem(key);
      } catch {
        // storage disabled
      }
      this.engine.subscribe(() => {
        const id = this.engine.participant?.id;
        if (id && id !== this.engine.resumeParticipantId) {
          this.engine.resumeParticipantId = id;
          try {
            localStorage.setItem(key, id);
          } catch {
            // storage disabled
          }
        }
      });
    }
  }

  start(): void {
    this.conn.start();
    this.trailTimer = setInterval(() => this.engine.expireTrail(2500), 1000);
  }

  stop(): void {
    this.conn.stop();
    if (this.lockTimer) clearInterval(this.lockTimer);
    if (this.trailTimer) clearInterval(this.trailTimer);
  }

  subscribe(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  bump(): void {
    this.version++;
    for (const fn of this.listeners) fn();
  }

  private reportError(e: ContractErrorBody, count: number): void {
    const text = ERROR_TEXT[e.code] ?? e.message;
    this.host.toast(count > 1 ? `${text} (${count} edits undone)` : text, "error");
  }

  get view(): Doc {
    return this.engine.view;
  }

  get rootId(): string {
    return this.view.map?.root_id ?? "";
  }

  /** The laid-out display model, memoised on everything it depends on. */
  get display(): Display {
    const e = this.engine;
    const key = `${e.docVersion}|${this.showGhosts}|${this.draft?.id ?? ""}|${this.draft?.title ?? ""}|${e.runs.size}|${runKey(e)}`;
    if (this.displayCache?.key === key) return this.displayCache.display;
    const t0 = performance.now();
    const display = buildDisplay(this.layoutEngine, {
      view: e.view, proposals: e.proposals.values(), runs: e.runs.values(), showGhosts: this.showGhosts, draft: this.draft,
    });
    this.lastLayoutMs = performance.now() - t0;
    this.displayCache = { key, display };
    return display;
  }

  // --- edits -----------------------------------------------------------------------

  /** Records undo, applies optimistically and submits. */
  edit(drafts: OpDraft[]): void {
    if (!drafts.length) return;
    this.undoStack.record(this.view, drafts);
    this.redoStack = [];
    this.engine.local(drafts);
  }

  node(id: string | null): MapNode | undefined {
    return id ? this.view.nodes.get(id) : undefined;
  }

  select(id: string | null): void {
    if (this.selection === id) return;
    this.selection = id;
    if (id) this.engine.markSeen(id);
    this.publishPresence({ selection: id ? [id] : [] });
    this.bump();
  }

  private newNode(parentId: string, order: string, kind: NodeKind = "idea", title = "New idea"): OpDraft {
    return { type: "node.create", payload: { node: { id: newId("nd"), parent_id: parentId, order, title, kind } } };
  }

  addChild(parentId = this.selection): string | null {
    const parent = this.node(parentId);
    if (!parent || parent.deleted) return null;
    const kids = childrenOf(this.view, parent.id);
    const d = this.newNode(parent.id, orderBetween(kids.at(-1)?.order ?? "", ""));
    const drafts: OpDraft[] = [d];
    if (parent.collapsed) drafts.push({ type: "node.update", payload: { id: parent.id, set: { collapsed: false } } });
    this.edit(drafts);
    const id = (d.payload as { node: { id: string } }).node.id;
    this.select(id);
    this.startEdit(id, undefined, true);
    return id;
  }

  addSibling(id = this.selection): string | null {
    const n = this.node(id);
    if (!n) return null;
    if (!n.parent_id) return this.addChild(n.id); // the root has no siblings
    const sibs = childrenOf(this.view, n.parent_id);
    const i = sibs.findIndex((s) => s.id === n.id);
    const d = this.newNode(n.parent_id, orderBetween(n.order, sibs[i + 1]?.order ?? ""));
    this.edit([d]);
    const nid = (d.payload as { node: { id: string } }).node.id;
    this.select(nid);
    this.startEdit(nid, undefined, true);
    return nid;
  }

  setTitle(id: string, title: string): boolean {
    const n = this.node(id);
    const t = title.replace(/[\r\n]+/g, " ").trim().slice(0, LIMITS.title);
    if (!n || !t || t === n.title) return false;
    this.edit([{ type: "node.update", payload: { id, set: { title: t } } }]);
    return true;
  }

  update(id: string, set: NonNullable<Extract<OpDraft, { type: "node.update" }>["payload"]["set"]>): void {
    if (!this.node(id)) return;
    this.edit([{ type: "node.update", payload: { id, set } }]);
  }

  setTags(id: string, tags: string[]): void {
    const clean = [...new Set(tags.map((t) => t.trim().toLowerCase().replace(/[\s,#]+/g, "-")).filter(Boolean))].slice(0, LIMITS.tags);
    this.update(id, { tags: clean });
  }

  /** Retire = tombstone the node and its subtree (undo restores it). */
  retire(id = this.selection): void {
    const n = this.node(id);
    if (!n) return;
    if (n.id === this.rootId) return this.host.toast(ERROR_TEXT.root_immutable!, "error");
    // Select a neighbour first so the keyboard keeps working.
    const sibs = n.parent_id ? childrenOf(this.view, n.parent_id) : [];
    const i = sibs.findIndex((s) => s.id === n.id);
    const next = sibs[i + 1] ?? sibs[i - 1];
    this.edit([{ type: "node.delete", payload: { id: n.id } }]);
    this.select(next?.id ?? n.parent_id ?? null);
  }

  toggleCollapse(id = this.selection): void {
    const n = this.node(id);
    if (!n || n.id === this.rootId) return;
    if (!n.collapsed && !childrenOf(this.view, n.id).length) return;
    this.edit([{ type: "node.update", payload: { id: n.id, set: { collapsed: !n.collapsed } } }]);
  }

  /** Re-parent or reorder; refuses cycles locally (the server re-checks). */
  move(id: string, parentId: string, order: string): boolean {
    const n = this.node(id);
    if (!n) return false;
    const idx = buildParentIndex(this.view.nodes.values());
    if (id === this.rootId || id === parentId) return false;
    let cur: string | undefined = parentId;
    while (cur) {
      if (cur === id) {
        this.host.toast(ERROR_TEXT.cycle!, "error");
        return false;
      }
      cur = idx.get(cur) || undefined;
    }
    if (n.parent_id === parentId && n.order === order) return false;
    const drafts: OpDraft[] = [{ type: "node.move", payload: { id, parent_id: parentId, order } }];
    const parent = this.node(parentId);
    if (parent?.collapsed) drafts.push({ type: "node.update", payload: { id: parentId, set: { collapsed: false } } });
    this.edit(drafts);
    return true;
  }

  private redoStack: OpDraft[][] = [];
  private serverUndo = true;
  private undoBusy = false;

  /**
   * Per-author undo through the protocol (§13 `POST .../undo`): the server
   * reverts your latest batch and broadcasts the inverse to everyone. Redo
   * re-submits the inverse of what was undone as a new batch. If the server
   * has no undo route, the client-side inverse stack is used instead.
   */
  async undo(): Promise<void> {
    if (this.undoBusy) return;
    this.undoBusy = true;
    try {
      await this.settled(3000);
      // Snapshot: the confirmed doc is updated in place when the undo ops land.
      const before = cloneDoc(this.view);
      if (this.serverUndo) {
        try {
          const res = await this.host.api.undo(this.mapId);
          this.undoStack.undo(before); // keep the local stack aligned
          if (!res.ops.length) return this.host.toast("Nothing to undo.", "info");
          this.engine.handle({ type: "ops", payload: res });
          this.redoStack.push(inverseOf(before, res.ops.map((o) => ({ type: o.type, payload: o.payload }) as OpDraft)));
          return;
        } catch (e) {
          const code = e instanceof ContractError ? e.code : "";
          if (code === "conflict") return this.host.toast("Nothing to undo.", "info");
          if (code !== "not_found" && code !== "not_implemented") return this.host.toast("Undo failed. Try again.", "error");
          this.serverUndo = false; // older backend: fall back to client-side inverses
        }
      }
      const drafts = this.undoStack.undo(before);
      if (drafts) {
        this.engine.local(drafts);
        this.redoStack.push(inverseOf(before, drafts));
      }
    } finally {
      this.undoBusy = false;
    }
  }

  redo(): void {
    const drafts = this.redoStack.pop();
    if (!drafts?.length) return;
    this.undoStack.record(this.view, drafts);
    this.engine.local(drafts);
  }

  /** Resolves once every local op is acked (undo must target a committed batch). */
  private settled(timeoutMs: number): Promise<void> {
    if (!this.engine.pending.length) return Promise.resolve();
    return new Promise((resolve) => {
      const t = setTimeout(done, timeoutMs);
      const unsub = this.engine.subscribe(() => {
        if (!this.engine.pending.length) done();
      });
      function done() {
        clearTimeout(t);
        unsub();
        resolve();
      }
    });
  }

  siblingsOf = (parentId: string): MapNode[] => childrenOf(this.view, parentId).sort(compareSiblings);

  // --- inline editing and soft locks ----------------------------------------------------

  /** Who else is editing this node's field, if anyone (soft lock). */
  lockedBy(nodeId: string, field: "title" | "body_md" = "title"): string | null {
    const now = Date.now();
    for (const p of this.engine.presence.values()) {
      if (p.participant_id === this.engine.participant?.id || !p.editing) continue;
      if (p.editing.node_id === nodeId && p.editing.field === field && Date.parse(p.editing.expires_at) > now) {
        return this.engine.participants.get(p.participant_id)?.name ?? "Someone";
      }
    }
    return null;
  }

  startEdit(id: string, initial?: string, fresh = false): boolean {
    const n = this.node(id);
    if (!n) return false;
    const who = this.lockedBy(id);
    if (who) {
      this.host.toast(`${who} is editing this node.`, "info");
      return false;
    }
    this.editing = { id, initial, fresh, touched: initial !== undefined };
    this.draft = { id, title: initial ?? n.title };
    this.lock(id, "title");
    this.bump();
    return true;
  }

  /** Live title while typing (laid out, not sent). */
  setDraft(title: string): void {
    if (!this.editing) return;
    this.editing.touched = true;
    this.draft = { id: this.editing.id, title };
    this.bump();
  }

  /**
   * Keys that arrive after editing started but before the editor mounted
   * (fast typing right after Tab/Enter): applied to the draft so none are lost.
   */
  typeAhead(key: string): boolean {
    const e = this.editing;
    if (!e) return false;
    const cur = this.draft?.title ?? "";
    if (key === "Backspace") this.setDraft(e.touched ? cur.slice(0, -1) : "");
    else if (key.length === 1) this.setDraft(e.touched ? cur + key : key);
    else return false;
    return true;
  }

  commitEdit(): void {
    const e = this.editing;
    if (!e) return;
    const title = this.draft?.title ?? "";
    this.editing = null;
    this.draft = null;
    this.unlock();
    this.setTitle(e.id, title);
    this.bump();
  }

  cancelEdit(): void {
    if (!this.editing) return;
    this.editing = null;
    this.draft = null;
    this.unlock();
    this.bump();
  }

  /** Soft lock via presence.editing, renewed every 5 s with a 15 s TTL (§7). */
  lock(nodeId: string, field: "title" | "body_md"): void {
    const renew = () =>
      this.publishPresence({
        editing: { node_id: nodeId, field, expires_at: new Date(Date.now() + LIMITS.softLockTtlMs).toISOString() },
      });
    renew();
    if (this.lockTimer) clearInterval(this.lockTimer);
    this.lockTimer = setInterval(renew, 5000);
  }

  unlock(): void {
    if (this.lockTimer) clearInterval(this.lockTimer);
    this.lockTimer = null;
    this.publishPresence({ editing: undefined });
  }

  // --- presence ------------------------------------------------------------------------

  publishPresence(patch: Partial<Omit<Presence, "participant_id" | "updated_at">>): void {
    this.presence = { ...this.presence, ...patch };
    for (const k of Object.keys(this.presence) as (keyof typeof this.presence)[]) {
      if (this.presence[k] === undefined) delete this.presence[k];
    }
    const pid = this.engine.participant?.id;
    if (!pid) return;
    this.conn.updatePresence({ ...this.presence, participant_id: pid, updated_at: new Date().toISOString() });
  }

  // --- proposals -------------------------------------------------------------------------

  async review(proposalId: string, req: ReviewProposalRequest): Promise<void> {
    try {
      const p = await this.host.api.reviewProposal(proposalId, req);
      this.engine.setProposal(p);
      const conflicts = p.items.filter((i) => i.status === "conflict").length;
      if (conflicts) this.host.toast(`${conflicts} proposed change(s) no longer apply.`, "error");
    } catch (e) {
      this.host.toast(e instanceof ContractError ? e.message : "Could not review the proposal.", "error");
    }
  }

  /** Accept or reject every pending item of every open proposal that touches a node. */
  async reviewNode(nodeId: string, accept: boolean): Promise<void> {
    for (const p of this.engine.proposals.values()) {
      if (p.status !== "open" && p.status !== "ready") continue;
      const ids = p.items.filter((i) => i.status === "pending" && (i.node_ids.includes(nodeId) || i.op.id === nodeId.replace(/^ghost:/, ""))).map((i) => i.op.id);
      if (ids.length) await this.review(p.id, accept ? { accept: ids } : { reject: ids });
    }
  }

  /** Drops cached card sizes (fonts finished loading) and lays out again. */
  relayoutAll(): void {
    this.layoutEngine = new TreeLayout();
    this.displayCache = null;
    this.bump();
  }

  toggleGhosts(): void {
    this.showGhosts = !this.showGhosts;
    this.bump();
  }
}

function runKey(e: SyncEngine): string {
  let k = "";
  for (const r of e.runs.values()) k += `${r.id}:${r.status}:${r.node_id};`;
  return k;
}
