// Pure geometry: edge paths, viewport culling, spatial navigation, hit tests.
import type { Bounds, Box } from "./tree";

export interface Camera {
  x: number; // screen px of world origin
  y: number;
  k: number; // zoom
}

export const ZOOM = { min: 0.03, max: 3 } as const;

export function clampZoom(k: number): number {
  return Math.min(ZOOM.max, Math.max(ZOOM.min, k));
}

/** World rect visible through a w×h viewport. */
export function viewportWorld(cam: Camera, w: number, h: number): Bounds {
  return { x: -cam.x / cam.k, y: -cam.y / cam.k, w: w / cam.k, h: h / cam.k };
}

export function screenToWorld(cam: Camera, sx: number, sy: number): { x: number; y: number } {
  return { x: (sx - cam.x) / cam.k, y: (sy - cam.y) / cam.k };
}

/** Zoom by factor around a screen point, keeping that point fixed. */
export function zoomAt(cam: Camera, sx: number, sy: number, factor: number): Camera {
  const k = clampZoom(cam.k * factor);
  const f = k / cam.k;
  return { k, x: sx - (sx - cam.x) * f, y: sy - (sy - cam.y) * f };
}

/** Camera that fits bounds into a w×h viewport with padding, never zooming past 1. */
export function fitCamera(b: Bounds, w: number, h: number, pad = 48): Camera {
  if (b.w <= 0 || b.h <= 0) return { x: w / 2, y: h / 2, k: 1 };
  const k = clampZoom(Math.min(1, (w - pad * 2) / b.w, (h - pad * 2) / b.h));
  return { k, x: w / 2 - (b.x + b.w / 2) * k, y: h / 2 - (b.y + b.h / 2) * k };
}

/** Camera that brings a box into view with minimal movement. */
export function revealBox(cam: Camera, b: Box, w: number, h: number, pad = 40): Camera {
  const sx = b.x * cam.k + cam.x, sy = b.y * cam.k + cam.y;
  const sw = b.w * cam.k, sh = b.h * cam.k;
  let dx = 0, dy = 0;
  if (sx < pad) dx = pad - sx;
  else if (sx + sw > w - pad) dx = w - pad - (sx + sw);
  if (sy < pad) dy = pad - sy;
  else if (sy + sh > h - pad) dy = h - pad - (sy + sh);
  return dx || dy ? { ...cam, x: cam.x + dx, y: cam.y + dy } : cam;
}

export function intersects(a: Bounds, b: { x: number; y: number; w: number; h: number }): boolean {
  return a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
}

export function expand(b: Bounds, fraction: number): Bounds {
  const dx = b.w * fraction, dy = b.h * fraction;
  return { x: b.x - dx, y: b.y - dy, w: b.w + 2 * dx, h: b.h + 2 * dy };
}

export function contains(outer: Bounds, inner: Bounds): boolean {
  return inner.x >= outer.x && inner.y >= outer.y && inner.x + inner.w <= outer.x + outer.w &&
    inner.y + inner.h <= outer.y + outer.h;
}

/**
 * Offscreen virtualisation: ids of boxes intersecting `region`, plus any ids
 * in `always` (selection, the node being edited) so focus never unmounts.
 */
export function cull(boxes: Iterable<Box>, region: Bounds, always: Iterable<string> = []): Set<string> {
  const out = new Set<string>();
  for (const b of boxes) if (intersects(region, b)) out.add(b.id);
  for (const id of always) out.add(id);
  return out;
}

/** Tree edge from a parent card's side to a child card's side, as an SVG cubic path. */
export function treeEdgePath(p: Box, c: Box): string {
  const right = c.side >= 0;
  const x1 = p.depth === 0 ? p.x + p.w / 2 + (right ? p.w / 2 - 6 : -p.w / 2 + 6) : right ? p.x + p.w : p.x;
  const y1 = p.y + p.h / 2;
  const x2 = right ? c.x : c.x + c.w;
  const y2 = c.y + c.h / 2;
  const mx = (x1 + x2) / 2;
  return `M${r(x1)} ${r(y1)}C${r(mx)} ${r(y1)} ${r(mx)} ${r(y2)} ${r(x2)} ${r(y2)}`;
}

/** Cross-link: a curve between card centres, bowed to one side. Returns the path and its label point. */
export function linkPath(a: Box, b: Box): { d: string; lx: number; ly: number } {
  const x1 = a.x + a.w / 2, y1 = a.y + a.h / 2, x2 = b.x + b.w / 2, y2 = b.y + b.h / 2;
  const mx = (x1 + x2) / 2, my = (y1 + y2) / 2;
  const dx = x2 - x1, dy = y2 - y1;
  const len = Math.hypot(dx, dy) || 1;
  const bow = Math.min(120, len * 0.25);
  const cx = mx - (dy / len) * bow, cy = my + (dx / len) * bow;
  // Point on the quadratic at t=0.5.
  const lx = 0.25 * x1 + 0.5 * cx + 0.25 * x2, ly = 0.25 * y1 + 0.5 * cy + 0.25 * y2;
  return { d: `M${r(x1)} ${r(y1)}Q${r(cx)} ${r(cy)} ${r(x2)} ${r(y2)}`, lx, ly };
}

/** Straight arrow between two boxes' centres, trimmed to their edges (ghost moves). */
export function arrowPath(a: Box, b: Box): string {
  const x1 = a.x + a.w / 2, y1 = a.y + a.h / 2, x2 = b.x + b.w / 2, y2 = b.y + b.h / 2;
  const t1 = edgeT(a, x2 - x1, y2 - y1), t2 = edgeT(b, x1 - x2, y1 - y2);
  const sx = x1 + (x2 - x1) * t1, sy = y1 + (y2 - y1) * t1;
  const ex = x2 + (x1 - x2) * t2, ey = y2 + (y1 - y2) * t2;
  return `M${r(sx)} ${r(sy)}L${r(ex)} ${r(ey)}`;
}

function edgeT(b: Box, dx: number, dy: number): number {
  const tx = dx ? b.w / 2 / Math.abs(dx) : Infinity, ty = dy ? b.h / 2 / Math.abs(dy) : Infinity;
  return Math.min(1, tx, ty);
}

const r = (n: number) => Math.round(n * 10) / 10;

export type Dir = "left" | "right" | "up" | "down";

/**
 * Spatial arrow-key navigation: the nearest box in the given direction,
 * favouring boxes aligned with the current one. Along the tree axis it
 * prefers the parent/child relationship, as XMind does.
 */
export function navigate(boxes: ReadonlyMap<string, Box>, children: ReadonlyMap<string, string[]>, from: string, dir: Dir): string | null {
  const b = boxes.get(from);
  if (!b) return null;
  // Tree-aware horizontal moves: outward goes to the middle child, inward to the parent.
  if (dir === "left" || dir === "right") {
    const outward = b.depth === 0 ? null : (dir === "right") === (b.side === 1);
    if (b.depth === 0) {
      const kids = (children.get(from) ?? []).map((id) => boxes.get(id)!).filter((c) => c && c.side === (dir === "right" ? 1 : -1));
      if (kids.length) return closestByY(kids, b.y + b.h / 2).id;
    } else if (outward) {
      const kids = (children.get(from) ?? []).map((id) => boxes.get(id)!).filter(Boolean);
      if (kids.length) return closestByY(kids, b.y + b.h / 2).id;
    } else if (b.parentId) return b.parentId;
  }
  const cx = b.x + b.w / 2, cy = b.y + b.h / 2;
  let best: string | null = null;
  let bestScore = Infinity;
  for (const o of boxes.values()) {
    if (o.id === from) continue;
    const ox = o.x + o.w / 2, oy = o.y + o.h / 2;
    const dx = ox - cx, dy = oy - cy;
    let main: number, cross: number;
    if (dir === "right") (main = dx, cross = Math.abs(dy));
    else if (dir === "left") (main = -dx, cross = Math.abs(dy));
    else if (dir === "down") (main = dy, cross = Math.abs(dx));
    else (main = -dy, cross = Math.abs(dx));
    if (main <= 1) continue;
    // Siblings in the same column win over far-away cards for vertical moves.
    const sameColumn = (dir === "up" || dir === "down") && o.side === b.side && o.depth === b.depth ? 0.5 : 1;
    const score = (main + cross * 3) * sameColumn;
    if (score < bestScore) (bestScore = score, (best = o.id));
  }
  return best;
}

function closestByY(list: Box[], y: number): Box {
  let best = list[0]!;
  for (const c of list) if (Math.abs(c.y + c.h / 2 - y) < Math.abs(best.y + best.h / 2 - y)) best = c;
  return best;
}

/** The box under a world point, if any. */
export function hitTest(boxes: Iterable<Box>, x: number, y: number, skip?: (id: string) => boolean): Box | null {
  for (const b of boxes) {
    if (skip?.(b.id)) continue;
    if (x >= b.x && x <= b.x + b.w && y >= b.y && y <= b.y + b.h) return b;
  }
  return null;
}
