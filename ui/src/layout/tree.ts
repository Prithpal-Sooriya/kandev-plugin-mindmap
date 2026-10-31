// Balanced two-sided tidy tree, XMind "mind map" style: the central topic sits
// at the origin, first-level branches are split between the right and left
// sides so both sides have about the same height, and every subtree occupies
// its own vertical band so branches never overlap.
//
// Incremental: card sizes are cached per node (keyed by what affects them),
// and Box objects whose geometry did not change are reused by reference, so
// the renderer can skip every unchanged card. A full pass is O(n log n) and
// stays well under a frame for thousands of nodes (see tests/layout.bench.ts).
import { compareSiblings } from "../contract";
import { measureCard } from "./measure";

export const GAP = { h: 40, rootH: 56, v: 10 } as const;

export interface LayoutNode {
  id: string;
  parent_id?: string;
  order: string;
  title: string;
  collapsed?: boolean;
  deleted?: boolean;
}

export type Side = 1 | -1 | 0;

export interface Box {
  readonly id: string;
  readonly parentId: string;
  readonly x: number; // top-left, world px
  readonly y: number;
  readonly w: number;
  readonly h: number;
  readonly side: Side;
  readonly depth: number;
  /** Live descendants hidden by a collapse on this node (0 when expanded). */
  readonly hidden: number;
  readonly lines: number;
}

export interface Bounds {
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface LayoutResult {
  boxes: Map<string, Box>;
  /** Boxes that are new or moved/resized since the previous run. */
  changed: string[];
  removed: string[];
  bounds: Bounds;
  /** Visible children per node in display order (right side top-down, then left side top-down). */
  children: Map<string, string[]>;
}

export interface LayoutOptions {
  /** Whether a node shows a badge row (affects its height). */
  hasBadges?: (n: LayoutNode) => boolean;
}

interface Size {
  key: string;
  w: number;
  h: number;
  lines: number;
}

export class TreeLayout {
  private sizes = new Map<string, Size>();
  private prev = new Map<string, Box>();

  layout(nodes: ReadonlyMap<string, LayoutNode>, rootId: string, opts: LayoutOptions = {}): LayoutResult {
    const hasBadges = opts.hasBadges ?? (() => false);
    const kids = new Map<string, LayoutNode[]>();
    for (const n of nodes.values()) {
      if (n.deleted || !n.parent_id || n.id === rootId) continue;
      let a = kids.get(n.parent_id);
      if (!a) kids.set(n.parent_id, (a = []));
      a.push(n);
    }
    for (const a of kids.values()) a.sort(compareSiblings);

    const size = (n: LayoutNode): Size => {
      const isRoot = n.id === rootId;
      const badges = hasBadges(n);
      const key = `${isRoot ? 1 : 0}${badges ? 1 : 0}${n.title}`;
      const cached = this.sizes.get(n.id);
      if (cached && cached.key === key) return cached;
      const m = measureCard({ title: n.title, isRoot, badges });
      const s = { key, ...m };
      this.sizes.set(n.id, s);
      return s;
    };

    // Pass 1: subtree extents (vertical band height), bottom-up via explicit stack.
    const ext = new Map<string, number>();
    const hiddenCount = new Map<string, number>();
    const visibleKids = (id: string): LayoutNode[] => {
      const n = nodes.get(id);
      return n?.collapsed && id !== rootId ? [] : (kids.get(id) ?? []);
    };
    const root = nodes.get(rootId);
    const boxes = new Map<string, Box>();
    const children = new Map<string, string[]>();
    if (!root || root.deleted) return this.finish(boxes, children);

    const order: string[] = [];
    const stack = [rootId];
    while (stack.length) {
      const id = stack.pop()!;
      order.push(id);
      for (const c of visibleKids(id)) stack.push(c.id);
    }
    for (let i = order.length - 1; i >= 0; i--) {
      const id = order[i]!;
      const n = nodes.get(id)!;
      const vk = visibleKids(id);
      let sum = 0;
      for (const c of vk) sum += ext.get(c.id)!;
      if (vk.length) sum += GAP.v * (vk.length - 1);
      ext.set(id, Math.max(size(n).h, sum));
      if (n.collapsed && id !== rootId) hiddenCount.set(id, countLive(kids, id));
    }

    // Pass 2: split first-level branches into right (first k) and left (rest).
    const first = kids.get(rootId) ?? [];
    const k = balancedSplit(first.map((c) => ext.get(c.id)!));
    const right = first.slice(0, k);
    // Left side runs clockwise: the last branch sits at the top.
    const left = first.slice(k).reverse();
    children.set(rootId, [...right.map((c) => c.id), ...left.map((c) => c.id)]);

    const rs = size(root);
    this.put(boxes, root, "", -rs.w / 2, -rs.h / 2, rs, 0, 0, hiddenCount);

    const place = (list: LayoutNode[], parentId: string, side: 1 | -1, depth: number, edgeX: number, cy: number) => {
      let total = 0;
      for (const c of list) total += ext.get(c.id)!;
      total += GAP.v * Math.max(0, list.length - 1);
      let top = cy - total / 2;
      for (const c of list) {
        const e = ext.get(c.id)!;
        const s = size(c);
        const ccy = top + e / 2;
        const x = side === 1 ? edgeX : edgeX - s.w;
        this.put(boxes, c, parentId, x, ccy - s.h / 2, s, side, depth, hiddenCount);
        const vk = visibleKids(c.id);
        if (vk.length) {
          children.set(c.id, vk.map((v) => v.id));
          place(vk, c.id, side, depth + 1, side === 1 ? x + s.w + GAP.h : x - GAP.h, ccy);
        }
        top += e + GAP.v;
      }
    };
    place(right, rootId, 1, 1, rs.w / 2 + GAP.rootH, 0);
    place(left, rootId, -1, 1, -rs.w / 2 - GAP.rootH, 0);
    return this.finish(boxes, children);
  }

  private put(
    boxes: Map<string, Box>, n: LayoutNode, parentId: string, x: number, y: number, s: Size, side: Side, depth: number,
    hidden: Map<string, number>,
  ): void {
    const hc = hidden.get(n.id) ?? 0;
    const p = this.prev.get(n.id);
    if (
      p && p.x === x && p.y === y && p.w === s.w && p.h === s.h && p.side === side && p.depth === depth &&
      p.parentId === parentId && p.hidden === hc && p.lines === s.lines
    ) {
      boxes.set(n.id, p);
      return;
    }
    boxes.set(n.id, { id: n.id, parentId, x, y, w: s.w, h: s.h, side, depth, hidden: hc, lines: s.lines });
  }

  private finish(boxes: Map<string, Box>, children: Map<string, string[]>): LayoutResult {
    const changed: string[] = [];
    let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
    for (const b of boxes.values()) {
      if (this.prev.get(b.id) !== b) changed.push(b.id);
      if (b.x < minX) minX = b.x;
      if (b.y < minY) minY = b.y;
      if (b.x + b.w > maxX) maxX = b.x + b.w;
      if (b.y + b.h > maxY) maxY = b.y + b.h;
    }
    const removed: string[] = [];
    for (const id of this.prev.keys()) if (!boxes.has(id)) removed.push(id);
    for (const id of removed) if (!boxes.has(id)) this.sizes.delete(id);
    this.prev = boxes;
    const bounds = boxes.size ? { x: minX, y: minY, w: maxX - minX, h: maxY - minY } : { x: 0, y: 0, w: 0, h: 0 };
    return { boxes, changed, removed, bounds, children };
  }
}

function countLive(kids: Map<string, LayoutNode[]>, id: string): number {
  let n = 0;
  const stack = [id];
  while (stack.length) {
    for (const c of kids.get(stack.pop()!) ?? []) {
      n++;
      stack.push(c.id);
    }
  }
  return n;
}

/**
 * Index k so that weights[0..k) (right) and weights[k..) (left) are as close
 * as possible in total. Ties prefer the right side, which keeps a single
 * branch on the right like XMind.
 */
export function balancedSplit(weights: number[]): number {
  const total = weights.reduce((a, b) => a + b, 0);
  let best = weights.length > 0 ? 1 : 0;
  let bestDiff = Infinity;
  let acc = 0;
  for (let k = 0; k <= weights.length; k++) {
    const diff = Math.abs(acc - (total - acc));
    if (k > 0 && diff < bestDiff) {
      bestDiff = diff;
      best = k;
    }
    if (k < weights.length) acc += weights[k]!;
  }
  return best;
}
