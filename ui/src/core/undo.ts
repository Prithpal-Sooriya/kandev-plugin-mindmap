// Per-author undo/redo (protocol §7): undo never rewinds the log; it submits
// the inverse of your own edit as new ops, so later edits by others survive.
// Inverses are computed from the view just before the edit is applied.
import type { NodeFields } from "../contract";
import type { Doc } from "./doc";
import type { OpDraft } from "./sync";

const SETTABLE: (keyof NodeFields)[] = [
  "title", "body_md", "kind", "tags", "confidence", "status", "run_id", "color", "collapsed", "doc",
];
const CLEARABLE = new Set(["confidence", "color", "run_id", "body_md"]);

/** The drafts that undo `drafts` when applied after them, given the doc before them. */
export function inverseOf(before: Doc, drafts: OpDraft[]): OpDraft[] {
  const out: OpDraft[] = [];
  for (const d of drafts) {
    const inv = inverseOne(before, d);
    if (inv) out.unshift(...inv);
  }
  return out;
}

function inverseOne(doc: Doc, d: OpDraft): OpDraft[] | null {
  switch (d.type) {
    case "node.create":
      return [{ type: "node.delete", payload: { id: d.payload.node.id } }];
    case "node.delete":
      return doc.nodes.has(d.payload.id) ? [{ type: "node.restore", payload: { id: d.payload.id } }] : null;
    case "node.restore":
      return [{ type: "node.delete", payload: { id: d.payload.id } }];
    case "node.move": {
      const n = doc.nodes.get(d.payload.id);
      if (!n?.parent_id) return null;
      return [{ type: "node.move", payload: { id: n.id, parent_id: n.parent_id, order: n.order } }];
    }
    case "node.update": {
      const n = doc.nodes.get(d.payload.id);
      if (!n) return null;
      const p = d.payload;
      const set: NodeFields = {};
      const clear: ("confidence" | "color" | "run_id" | "body_md")[] = [];
      const touched = new Set<keyof NodeFields>();
      for (const k of SETTABLE) if (p.set && p.set[k] !== undefined) touched.add(k);
      for (const k of p.clear ?? []) touched.add(k);
      if (p.append_body) touched.add("body_md");
      for (const k of touched) {
        const prev = (n as Record<string, unknown>)[k];
        if (prev === undefined) {
          if (CLEARABLE.has(k)) clear.push(k as (typeof clear)[number]);
          else if (k === "collapsed") set.collapsed = false;
          else if (k === "tags") set.tags = [];
        } else (set as Record<string, unknown>)[k] = Array.isArray(prev) ? [...prev] : prev;
      }
      const add_tags = (p.remove_tags ?? []).filter((t) => n.tags?.includes(t));
      const remove_tags = (p.add_tags ?? []).filter((t) => !n.tags?.includes(t));
      const inv: OpDraft = {
        type: "node.update",
        payload: {
          id: n.id,
          ...(Object.keys(set).length ? { set } : {}),
          ...(clear.length ? { clear } : {}),
          ...(add_tags.length ? { add_tags } : {}),
          ...(remove_tags.length ? { remove_tags } : {}),
        },
      };
      return Object.keys(inv.payload).length > 1 ? [inv] : null;
    }
    case "link.create":
      return [{ type: "link.delete", payload: { id: d.payload.link.id } }];
    case "link.update": {
      const l = doc.links.get(d.payload.id);
      if (!l) return null;
      return [{ type: "link.update", payload: { id: l.id, label: l.label ?? "", reason: l.reason ?? "" } }];
    }
    case "map.update": {
      if (!doc.map || d.payload.title === undefined) return null;
      return [{ type: "map.update", payload: { title: doc.map.title } }];
    }
    default:
      return null; // merge, claims, answers, link delete and assets are not undoable from the canvas
  }
}

interface Entry {
  drafts: OpDraft[];
}

/** Undo and redo stacks of the local author's edits. */
export class UndoStack {
  private undoS: Entry[] = [];
  private redoS: Entry[] = [];
  constructor(private limit = 200) {}

  /** Records an edit (call with the doc before applying it). */
  record(before: Doc, drafts: OpDraft[]): void {
    const inv = inverseOf(before, drafts);
    if (!inv.length) return;
    this.undoS.push({ drafts: inv });
    if (this.undoS.length > this.limit) this.undoS.shift();
    this.redoS = [];
  }

  get canUndo(): boolean {
    return this.undoS.length > 0;
  }
  get canRedo(): boolean {
    return this.redoS.length > 0;
  }

  /** Pops the next undo; returns the drafts to submit. `before` is the current view. */
  undo(before: Doc): OpDraft[] | null {
    const e = this.undoS.pop();
    if (!e) return null;
    this.redoS.push({ drafts: inverseOf(before, e.drafts) });
    return e.drafts;
  }

  redo(before: Doc): OpDraft[] | null {
    const e = this.redoS.pop();
    if (!e) return null;
    this.undoS.push({ drafts: inverseOf(before, e.drafts) });
    return e.drafts;
  }
}
