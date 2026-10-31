// The map document and the op reducer. Node and link objects are immutable:
// every op that touches a node replaces it with a new object, so the layout
// and the renderer can detect changes by reference.
import {
  compareSiblings, orderBetween, type Link, type MapNode, type MindMap, type Op, type OpPayload, type Snapshot,
} from "../contract";

export interface Doc {
  map: MindMap | null;
  rev: number;
  nodes: Map<string, MapNode>;
  links: Map<string, Link>;
}

export function emptyDoc(): Doc {
  return { map: null, rev: 0, nodes: new Map(), links: new Map() };
}

export function docFromSnapshot(s: Snapshot): Doc {
  const nodes = new Map<string, MapNode>();
  for (const n of s.nodes) nodes.set(n.id, n);
  const links = new Map<string, Link>();
  for (const l of s.links) links.set(l.id, l);
  return { map: s.map, rev: s.rev, nodes, links };
}

/** Shallow copy: new maps, shared (immutable) node and link objects. */
export function cloneDoc(d: Doc): Doc {
  return { map: d.map, rev: d.rev, nodes: new Map(d.nodes), links: new Map(d.links) };
}

const nowIso = () => new Date().toISOString().replace(/\.\d+Z$/, "Z");

/** Live children of a node, sorted as the server sorts siblings. */
export function childrenOf(d: Doc, id: string): MapNode[] {
  const out: MapNode[] = [];
  for (const n of d.nodes.values()) if (n.parent_id === id && !n.deleted) out.push(n);
  return out.sort(compareSiblings);
}

/** parent id -> sorted live children, for the whole doc in one pass. */
export function childIndex(d: Doc): Map<string, MapNode[]> {
  const idx = new Map<string, MapNode[]>();
  for (const n of d.nodes.values()) {
    if (n.deleted || !n.parent_id) continue;
    let arr = idx.get(n.parent_id);
    if (!arr) idx.set(n.parent_id, (arr = []));
    arr.push(n);
  }
  for (const arr of idx.values()) arr.sort(compareSiblings);
  return idx;
}

export function rootId(d: Doc): string {
  return d.map?.root_id ?? "";
}

function patch(d: Doc, id: string, fn: (n: MapNode) => MapNode): void {
  const n = d.nodes.get(id);
  if (n) d.nodes.set(id, fn(n));
}

function subtreeIds(d: Doc, id: string): string[] {
  const kids = new Map<string, string[]>();
  for (const n of d.nodes.values()) {
    if (!n.parent_id) continue;
    let a = kids.get(n.parent_id);
    if (!a) kids.set(n.parent_id, (a = []));
    a.push(n.id);
  }
  const out: string[] = [];
  const stack = [id];
  while (stack.length) {
    const cur = stack.pop()!;
    out.push(cur);
    for (const k of kids.get(cur) ?? []) stack.push(k);
  }
  return out;
}

/**
 * Applies one committed or optimistic op to the doc in place. Ops that no
 * longer apply (missing node) are ignored, which is what the optimistic layer
 * needs when a pending op's target was deleted by someone else.
 */
export function applyOp(d: Doc, op: Op): void {
  const ts = op.ts ?? nowIso();
  const stamp = (n: MapNode): MapNode => ({ ...n, rev: op.rev || n.rev, updated_at: ts });
  switch (op.type) {
    case "map.update": {
      if (!d.map) return;
      const p = op.payload;
      d.map = {
        ...d.map,
        ...(p.title !== undefined ? { title: p.title } : {}),
        ...(p.repo_id !== undefined ? { repo_id: p.repo_id || undefined } : {}),
        ...(p.settings !== undefined ? { settings: p.settings } : {}),
      };
      return;
    }
    case "node.create": {
      const init = op.payload.node;
      if (d.nodes.has(init.id)) return;
      const n: MapNode = {
        id: init.id, map_id: op.map_id, parent_id: init.parent_id, order: init.order, title: init.title,
        kind: init.kind, status: init.status ?? "idle", author: op.actor, rev: op.rev, created_at: ts, updated_at: ts,
      };
      if (init.body_md) n.body_md = init.body_md;
      if (init.tags?.length) n.tags = [...init.tags];
      if (init.confidence !== undefined) n.confidence = init.confidence;
      if (init.sources?.length) n.sources = [...init.sources];
      if (init.color) n.color = init.color;
      if (init.collapsed) n.collapsed = true;
      if (init.question) n.question = init.question;
      if (init.doc) n.doc = init.doc;
      d.nodes.set(n.id, n);
      return;
    }
    case "node.update":
      return patch(d, op.payload.id, (n) => applyUpdate(stamp(n), op.payload));
    case "node.move": {
      const p = op.payload;
      return patch(d, p.id, (n) => ({ ...stamp(n), parent_id: p.parent_id, order: p.order }));
    }
    case "node.delete":
      for (const id of subtreeIds(d, op.payload.id)) patch(d, id, (n) => ({ ...stamp(n), deleted: true }));
      return;
    case "node.restore":
      for (const id of subtreeIds(d, op.payload.id)) patch(d, id, (n) => {
        const { deleted: _, ...rest } = stamp(n);
        return rest;
      });
      return;
    case "node.merge":
      return applyMerge(d, op.payload, stamp);
    case "node.claim":
      return patch(d, op.payload.id, (n) => ({ ...stamp(n), claim: op.payload.claim }));
    case "node.release":
      return patch(d, op.payload.id, (n) => {
        const { claim: _, ...rest } = stamp(n);
        return rest;
      });
    case "question.answer":
      return patch(d, op.payload.node_id, (n) =>
        n.question
          ? {
              ...stamp(n),
              question: { ...n.question, answer: op.payload.answer, answered_by: op.actor.participant_id, answered_at: ts },
            }
          : n,
      );
    case "link.create": {
      const l = op.payload.link;
      if (d.links.has(l.id)) return;
      d.links.set(l.id, { ...l, map_id: op.map_id, author: op.actor, created_at: ts });
      return;
    }
    case "link.update": {
      const p = op.payload;
      const l = d.links.get(p.id);
      if (!l) return;
      d.links.set(p.id, {
        ...l, ...(p.label !== undefined ? { label: p.label } : {}), ...(p.reason !== undefined ? { reason: p.reason } : {}),
      });
      return;
    }
    case "link.delete": {
      const l = d.links.get(op.payload.id);
      if (l) d.links.set(l.id, { ...l, deleted: true });
      return;
    }
    case "asset.attach": {
      const a = op.payload.asset;
      return patch(d, a.node_id, (n) =>
        n.assets?.some((x) => x.id === a.id) ? n : { ...stamp(n), assets: [...(n.assets ?? []), a] },
      );
    }
    case "asset.detach": {
      const p = op.payload;
      return patch(d, p.node_id, (n) => ({ ...stamp(n), assets: (n.assets ?? []).filter((a) => a.id !== p.id) }));
    }
  }
}

function applyUpdate(n: MapNode, p: OpPayload<"node.update">): MapNode {
  const out: MapNode = { ...n };
  if (p.set) {
    for (const [k, v] of Object.entries(p.set)) {
      if (v !== undefined) (out as Record<string, unknown>)[k] = Array.isArray(v) ? [...v] : v;
    }
    if (p.set.run_id === "") delete out.run_id;
    if (p.set.color === "") delete out.color;
    if (out.collapsed === false) delete out.collapsed; // omitempty on the wire
  }
  for (const f of p.clear ?? []) delete (out as Record<string, unknown>)[f];
  if (p.append_body) out.body_md = out.body_md ? `${out.body_md}\n\n${p.append_body}` : p.append_body;
  if (p.add_tags?.length) {
    const tags = [...(out.tags ?? [])];
    for (const t of p.add_tags) if (!tags.includes(t)) tags.push(t);
    out.tags = tags;
  }
  if (p.remove_tags?.length) {
    out.tags = (out.tags ?? []).filter((t) => !p.remove_tags!.includes(t));
    if (!out.tags.length) delete out.tags;
  }
  if (p.add_sources?.length) {
    const srcs = [...(out.sources ?? [])];
    for (const s of p.add_sources) if (!srcs.some((x) => x.url === s.url)) srcs.push(s);
    out.sources = srcs;
  }
  return out;
}

// Mirrors the Go doc comment on NodeMergePayload. The server is authoritative;
// this keeps the optimistic and ghost views close to what it will commit.
function applyMerge(d: Doc, p: OpPayload<"node.merge">, stamp: (n: MapNode) => MapNode): void {
  const into = d.nodes.get(p.into_id);
  if (!into) return;
  let merged: MapNode = stamp(into);
  let last = childrenOf(d, p.into_id).at(-1)?.order ?? "";
  for (const id of p.ids) {
    const m = d.nodes.get(id);
    if (!m || m.deleted) continue;
    const body = `## ${m.title}${m.body_md ? `\n\n${m.body_md}` : ""}`;
    merged = applyUpdate(merged, { id: p.into_id, append_body: body, add_tags: m.tags, add_sources: m.sources });
    for (const c of childrenOf(d, id)) {
      last = orderBetween(last, "");
      d.nodes.set(c.id, { ...stamp(c), parent_id: p.into_id, order: last });
    }
    for (const l of d.links.values()) {
      if (l.from === id || l.to === id) {
        d.links.set(l.id, { ...l, from: l.from === id ? p.into_id : l.from, to: l.to === id ? p.into_id : l.to });
      }
    }
    d.nodes.set(id, { ...stamp(m), deleted: true });
  }
  d.nodes.set(p.into_id, merged);
}
