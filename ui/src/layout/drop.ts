// Drag-to-reparent: where a dragged card would land, and the order key to send.
import { isAncestor, orderBetween, type ParentIndex } from "../contract";
import type { Box } from "./tree";

export interface DropTarget {
  parentId: string;
  order: string;
  /** "child": drop onto a card; "before"/"after": insert next to a sibling. */
  mode: "child" | "before" | "after";
  targetId: string;
  /** Indicator geometry in world coordinates. */
  indicator: { x: number; y: number; w: number; h: number };
}

interface OrderedNode {
  id: string;
  order: string;
}

/**
 * Hovering the middle of a card drops the dragged node as its last child; the
 * top or bottom quarter inserts before or after it among its siblings. Drops
 * into the dragged subtree are refused (the server would answer `cycle`).
 */
export function dropTarget(
  boxes: ReadonlyMap<string, Box>,
  siblingsOf: (parentId: string) => OrderedNode[],
  parents: ParentIndex,
  dragged: string,
  x: number,
  y: number,
): DropTarget | null {
  for (const b of boxes.values()) {
    if (x < b.x || x > b.x + b.w || y < b.y || y > b.y + b.h) continue;
    if (b.id === dragged || isAncestor(parents, dragged, b.id)) return null;
    const rel = (y - b.y) / b.h;
    if (b.depth > 0 && (rel < 0.25 || rel > 0.75)) {
      const before = rel < 0.25;
      const sibs = siblingsOf(b.parentId).filter((s) => s.id !== dragged);
      const i = sibs.findIndex((s) => s.id === b.id);
      if (i < 0) return null;
      const lo = before ? (sibs[i - 1]?.order ?? "") : sibs[i]!.order;
      const hi = before ? sibs[i]!.order : (sibs[i + 1]?.order ?? "");
      return {
        parentId: b.parentId, order: orderBetween(lo, hi), mode: before ? "before" : "after", targetId: b.id,
        indicator: { x: b.x, y: before ? b.y - 5 : b.y + b.h + 3, w: b.w, h: 2 },
      };
    }
    const kids = siblingsOf(b.id).filter((s) => s.id !== dragged);
    return {
      parentId: b.id, order: orderBetween(kids.at(-1)?.order ?? "", ""), mode: "child", targetId: b.id,
      indicator: { x: b.x - 3, y: b.y - 3, w: b.w + 6, h: b.h + 6 },
    };
  }
  return null;
}
