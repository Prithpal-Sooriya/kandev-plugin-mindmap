// What the canvas draws: the optimistic view plus the ghost proposal layer,
// laid out. Pure, so it can be tested and benchmarked without a DOM.
import type { Link, MapNode, Op, Proposal, Run } from "../contract";
import { applyOp, cloneDoc, type Doc } from "../core/doc";
import { TreeLayout, type LayoutResult } from "../layout/tree";

export type GhostKind = "create" | "update" | "delete" | "move" | "merge" | "link";

export interface Ghost {
  kind: GhostKind;
  proposalId: string;
  opId: string;
  /** For moves and merges: the real node the arrow starts from. */
  fromId?: string;
  /** For updates: a short description of the change. */
  detail?: string;
}

export interface Display {
  nodes: Map<string, MapNode>;
  links: Link[];
  /** Ghost info keyed by display node id (ghost copies use "ghost:<opId>"). */
  ghosts: Map<string, Ghost[]>;
  ghostLinks: { link: Link; ghost: Ghost }[];
  /** Ghost arrows (moves, merges): from real node to ghost or target. */
  arrows: { from: string; to: string; ghost: Ghost }[];
  layout: LayoutResult;
  rootId: string;
  /** Live runs per node (several agents can work on one node). */
  runsByNode: Map<string, Run[]>;
  /** Pending proposal items touching each node. */
  pendingByNode: Map<string, number>;
}

const ACTIVE_PROPOSAL = new Set(["open", "ready"]);
const LIVE_RUN = new Set(["queued", "running", "waiting", "review", "failed", "done"]);

export interface DisplayInput {
  view: Doc;
  proposals: Iterable<Proposal>;
  runs: Iterable<Run>;
  showGhosts: boolean;
  /** Title being typed in the inline editor: laid out live, not yet sent. */
  draft?: { id: string; title: string } | null;
}

export function buildDisplay(layout: TreeLayout, input: DisplayInput): Display {
  const { view, showGhosts } = input;
  const ghosts = new Map<string, Ghost[]>();
  const arrows: Display["arrows"] = [];
  const ghostLinks: Display["ghostLinks"] = [];
  const pendingByNode = new Map<string, number>();
  let doc = view;
  const addGhost = (id: string, g: Ghost) => {
    const list = ghosts.get(id);
    if (list) list.push(g);
    else ghosts.set(id, [g]);
  };

  const proposals = [...input.proposals].filter((p) => ACTIVE_PROPOSAL.has(p.status));
  if (proposals.length) {
    for (const p of proposals) {
      for (const it of p.items) {
        if (it.status !== "pending") continue;
        for (const id of it.node_ids) pendingByNode.set(id, (pendingByNode.get(id) ?? 0) + 1);
      }
    }
  }
  if (showGhosts && proposals.length) {
    doc = cloneDoc(view);
    for (const p of proposals) {
      for (const it of p.items) {
        if (it.status !== "pending") continue;
        applyGhost(doc, p, it.op, addGhost, arrows, ghostLinks);
      }
    }
  }
  if (input.draft) {
    const n = doc.nodes.get(input.draft.id);
    if (n && n.title !== input.draft.title) {
      if (doc === view) doc = { ...view, nodes: new Map(view.nodes) };
      doc.nodes.set(n.id, { ...n, title: input.draft.title || " " });
    }
  }

  const runsByNode = new Map<string, Run[]>();
  for (const r of input.runs) {
    if (!LIVE_RUN.has(r.status)) continue;
    const list = runsByNode.get(r.node_id);
    if (list) list.push(r);
    else runsByNode.set(r.node_id, [r]);
  }

  const rootId = doc.map?.root_id ?? "";
  const result = layout.layout(doc.nodes, rootId, {
    hasBadges: (n) => {
      const m = n as MapNode;
      return !!(m.sources?.length || m.confidence !== undefined || m.assets?.length || m.tags?.length);
    },
  });
  const links = [...doc.links.values()].filter((l) => !l.deleted);
  return { nodes: doc.nodes, links, ghosts, ghostLinks, arrows, layout: result, rootId, runsByNode, pendingByNode };
}

function applyGhost(
  doc: Doc, p: Proposal, op: Op, addGhost: (id: string, g: Ghost) => void, arrows: Display["arrows"],
  ghostLinks: Display["ghostLinks"],
): void {
  const base = { proposalId: p.id, opId: op.id };
  switch (op.type) {
    case "node.create": {
      if (doc.nodes.has(op.payload.node.id)) return;
      applyOp(doc, { ...op, rev: 0 });
      addGhost(op.payload.node.id, { ...base, kind: "create" });
      return;
    }
    case "node.update": {
      const n = doc.nodes.get(op.payload.id);
      if (!n) return;
      const parts: string[] = [];
      if (op.payload.set?.title !== undefined) parts.push(`title → "${op.payload.set.title}"`);
      if (op.payload.set?.body_md !== undefined || op.payload.append_body) parts.push("body");
      if (op.payload.set?.kind) parts.push(`kind → ${op.payload.set.kind}`);
      if (op.payload.add_tags?.length || op.payload.remove_tags?.length || op.payload.set?.tags) parts.push("tags");
      if (op.payload.add_sources?.length) parts.push(`+${op.payload.add_sources.length} sources`);
      if (op.payload.set?.confidence !== undefined) parts.push(`confidence ${op.payload.set.confidence}`);
      addGhost(n.id, { ...base, kind: "update", detail: parts.join(", ") || "edit" });
      return;
    }
    case "node.delete":
      if (doc.nodes.has(op.payload.id)) addGhost(op.payload.id, { ...base, kind: "delete" });
      return;
    case "node.move": {
      const n = doc.nodes.get(op.payload.id);
      if (!n || !doc.nodes.has(op.payload.parent_id)) return;
      // A dashed copy where the node would land, and an arrow from the original.
      const ghostId = `ghost:${op.id}`;
      doc.nodes.set(ghostId, { ...n, id: ghostId, parent_id: op.payload.parent_id, order: op.payload.order, collapsed: true });
      const g: Ghost = { ...base, kind: "move", fromId: n.id };
      addGhost(ghostId, g);
      addGhost(n.id, g);
      arrows.push({ from: n.id, to: ghostId, ghost: g });
      return;
    }
    case "node.merge":
      for (const id of op.payload.ids) {
        if (!doc.nodes.has(id)) continue;
        const g: Ghost = { ...base, kind: "merge", fromId: id };
        addGhost(id, g);
        arrows.push({ from: id, to: op.payload.into_id, ghost: g });
      }
      return;
    case "link.create": {
      const l = op.payload.link;
      ghostLinks.push({
        link: { ...l, map_id: op.map_id, author: op.actor, created_at: p.created_at },
        ghost: { ...base, kind: "link" },
      });
      return;
    }
  }
}
