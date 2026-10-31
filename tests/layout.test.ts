import { describe, expect, it } from "vitest";
import { orderBetween } from "../ui/src/contract";
import { applyOp } from "../ui/src/core/doc";
import { dropTarget } from "../ui/src/layout/drop";
import {
  cull, fitCamera, intersects, linkPath, navigate, treeEdgePath, viewportWorld, zoomAt,
} from "../ui/src/layout/geometry";
import { measureCard, wrap } from "../ui/src/layout/measure";
import { balancedSplit, TreeLayout, type Box } from "../ui/src/layout/tree";
import { buildParentIndex, type Op } from "../ui/src/contract";
import { childrenOf } from "../ui/src/core/doc";
import { child, docFrom, ROOT, seededDoc } from "./fixtures";

const overlaps = (a: Box, b: Box) => a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;

describe("measure", () => {
  it("wraps and clamps titles deterministically", () => {
    expect(wrap("short", 220).lines).toBe(1);
    expect(wrap("a fairly long title that certainly needs more than one line of text to fit", 220).lines).toBeGreaterThan(1);
    const a = measureCard({ title: "Hello", isRoot: false, badges: false });
    expect(a).toEqual(measureCard({ title: "Hello", isRoot: false, badges: false }));
    const long = measureCard({ title: "word ".repeat(80), isRoot: false, badges: true });
    expect(long.lines).toBe(3);
    expect(long.h).toBeGreaterThan(a.h);
  });
});

describe("tree layout", () => {
  it("is deterministic: same doc, same positions", () => {
    const d = seededDoc(300, 7);
    const a = new TreeLayout().layout(d.nodes, ROOT);
    const b = new TreeLayout().layout(d.nodes, ROOT);
    expect([...a.boxes.values()]).toEqual([...b.boxes.values()]);
  });

  it("centres the root and puts children outside their parent on their side", () => {
    const d = seededDoc(200, 3);
    const { boxes } = new TreeLayout().layout(d.nodes, ROOT);
    const root = boxes.get(ROOT)!;
    expect(root.x + root.w / 2).toBeCloseTo(0);
    expect(root.y + root.h / 2).toBeCloseTo(0);
    for (const b of boxes.values()) {
      if (b.depth === 0) continue;
      const p = boxes.get(b.parentId)!;
      if (b.side === 1) expect(b.x).toBeGreaterThan(p.x + p.w);
      else expect(b.x + b.w).toBeLessThan(p.x);
      if (b.depth > 1) expect(b.side).toBe(p.side);
    }
  });

  it("never overlaps cards", () => {
    const d = seededDoc(400, 11);
    const list = [...new TreeLayout().layout(d.nodes, ROOT).boxes.values()];
    for (let i = 0; i < list.length; i++) {
      for (let j = i + 1; j < list.length; j++) expect(overlaps(list[i]!, list[j]!), `${list[i]!.id} vs ${list[j]!.id}`).toBe(false);
    }
  });

  it("balances the two sides", () => {
    expect(balancedSplit([10, 10, 10, 10])).toBe(2);
    expect(balancedSplit([30, 10, 10, 10])).toBe(1);
    expect(balancedSplit([5])).toBe(1); // a single branch goes right
    expect(balancedSplit([])).toBe(0);
    const d = seededDoc(500, 5);
    const { boxes } = new TreeLayout().layout(d.nodes, ROOT);
    const height = (side: number) => {
      let min = Infinity, max = -Infinity;
      for (const b of boxes.values()) if (b.side === side) (min = Math.min(min, b.y), (max = Math.max(max, b.y + b.h)));
      return max - min;
    };
    const r = height(1), l = height(-1);
    const biggestBranch = Math.max(...childrenOf(d, ROOT).map((c) => {
      let min = Infinity, max = -Infinity;
      const stack = [c.id];
      while (stack.length) {
        const id = stack.pop()!;
        const b = boxes.get(id);
        if (b) (min = Math.min(min, b.y), (max = Math.max(max, b.y + b.h)));
        for (const k of childrenOf(d, id)) stack.push(k.id);
      }
      return max - min;
    }));
    expect(Math.abs(r - l)).toBeLessThanOrEqual(biggestBranch);
  });

  it("orders siblings top to bottom on the right, clockwise on the left", () => {
    const d = docFrom([
      child(ROOT, "R1", "1", "nd_r1aaaaaaaaaa"), child(ROOT, "R2", "2", "nd_r2aaaaaaaaaa"),
      child(ROOT, "L1", "3", "nd_l1aaaaaaaaaa"), child(ROOT, "L2", "4", "nd_l2aaaaaaaaaa"),
    ]);
    const { boxes } = new TreeLayout().layout(d.nodes, ROOT);
    const b = (id: string) => boxes.get(id)!;
    expect(b("nd_r1aaaaaaaaaa").side).toBe(1);
    expect(b("nd_r2aaaaaaaaaa").side).toBe(1);
    expect(b("nd_r1aaaaaaaaaa").y).toBeLessThan(b("nd_r2aaaaaaaaaa").y);
    expect(b("nd_l1aaaaaaaaaa").side).toBe(-1);
    expect(b("nd_l2aaaaaaaaaa").y).toBeLessThan(b("nd_l1aaaaaaaaaa").y);
  });

  it("hides collapsed subtrees and counts them", () => {
    const d = docFrom([
      child(ROOT, "A", "V", "nd_aaaaaaaaaaaa"), child("nd_aaaaaaaaaaaa", "A1", "V", "nd_a1aaaaaaaaaa"),
      child("nd_a1aaaaaaaaaa", "A11", "V", "nd_a11aaaaaaaaa"),
    ]);
    const a = d.nodes.get("nd_aaaaaaaaaaaa")!;
    d.nodes.set(a.id, { ...a, collapsed: true });
    const { boxes } = new TreeLayout().layout(d.nodes, ROOT);
    expect(boxes.has("nd_a1aaaaaaaaaa")).toBe(false);
    expect(boxes.get(a.id)!.hidden).toBe(2);
  });

  it("is incremental: unchanged boxes are reused by reference", () => {
    const d = seededDoc(300, 2);
    const L = new TreeLayout();
    const first = L.layout(d.nodes, ROOT);
    expect(first.changed.length).toBe(first.boxes.size);
    const same = L.layout(d.nodes, ROOT);
    expect(same.changed).toEqual([]);
    // A title edit that keeps the height only moves that card's own subtree.
    const leaf = [...first.boxes.values()].find((b) => b.depth >= 2 && !childrenOf(d, b.id).length)!;
    const n = d.nodes.get(leaf.id)!;
    applyOp(d, { id: "op_aaaaaaaaaaaa", map_id: "map_test00000001", rev: 999, actor: { participant_id: "pt_aaaaaaaaaaaa" }, type: "node.update", payload: { id: n.id, set: { title: `${n.title}!` } } } as Op);
    const next = L.layout(d.nodes, ROOT);
    expect(next.changed.length).toBeLessThan(5);
    expect(next.boxes.get(ROOT)).toBe(first.boxes.get(ROOT));
  });
});

describe("geometry", () => {
  const d = seededDoc(120, 4);
  const { boxes, children } = new TreeLayout().layout(d.nodes, ROOT);

  it("culls to the viewport and always keeps pinned ids", () => {
    const cam = { x: 400, y: 300, k: 1 };
    const region = viewportWorld(cam, 800, 600);
    const vis = cull(boxes.values(), region, ["nd_seed000000000119"]);
    expect(vis.has(ROOT)).toBe(true);
    for (const id of vis) if (id !== "nd_seed000000000119") expect(intersects(region, boxes.get(id)!)).toBe(true);
    for (const b of boxes.values()) if (intersects(region, b)) expect(vis.has(b.id)).toBe(true);
    expect(vis.has("nd_seed000000000119")).toBe(true);
    expect(vis.size).toBeLessThan(boxes.size);
    // Far away: nothing but the pinned id.
    expect(cull(boxes.values(), { x: 1e6, y: 1e6, w: 10, h: 10 }, []).size).toBe(0);
  });

  it("zooms around the cursor and fits the bounds", () => {
    const cam = zoomAt({ x: 0, y: 0, k: 1 }, 100, 50, 2);
    expect(cam).toEqual({ k: 2, x: -100, y: -50 });
    const fit = fitCamera({ x: -500, y: -200, w: 1000, h: 400 }, 1000, 600);
    expect(fit.k).toBeLessThan(1);
    expect(fit.x).toBeCloseTo(500);
  });

  it("navigates spatially, tree-aware on the horizontal axis", () => {
    const right = [...boxes.values()].find((b) => b.depth === 1 && b.side === 1 && (children.get(b.id)?.length ?? 0) > 1)!;
    expect(navigate(boxes, children, right.id, "left")).toBe(ROOT);
    const kid = navigate(boxes, children, right.id, "right")!;
    expect(boxes.get(kid)!.parentId).toBe(right.id);
    expect(navigate(boxes, children, ROOT, "right")).toSatisfy((id: string) => boxes.get(id)!.side === 1);
    expect(navigate(boxes, children, ROOT, "left")).toSatisfy((id: string) => boxes.get(id)!.side === -1);
    const kids = children.get(right.id)!;
    expect(navigate(boxes, children, kids[0]!, "down")).toBe(kids[1]);
    expect(navigate(boxes, children, kids[1]!, "up")).toBe(kids[0]);
  });

  it("draws edges and links as SVG paths", () => {
    const b = [...boxes.values()].find((x) => x.depth === 2)!;
    expect(treeEdgePath(boxes.get(b.parentId)!, b)).toMatch(/^M[-\d.]+ [-\d.]+C/);
    expect(linkPath(boxes.get(ROOT)!, b).d).toMatch(/Q/);
  });

  it("computes drop targets and refuses drops into the dragged subtree", () => {
    const parents = buildParentIndex(d.nodes.values());
    const sib = (p: string) => childrenOf(d, p);
    const a = [...boxes.values()].find((x) => x.depth === 1 && (children.get(x.id)?.length ?? 0) > 0)!;
    const other = [...boxes.values()].find((x) => x.depth === 1 && x.id !== a.id)!;
    const onto = dropTarget(boxes, sib, parents, a.id, other.x + other.w / 2, other.y + other.h / 2)!;
    expect(onto.mode).toBe("child");
    expect(onto.parentId).toBe(other.id);
    const inner = boxes.get(children.get(a.id)![0]!)!;
    expect(dropTarget(boxes, sib, parents, a.id, inner.x + 2, inner.y + inner.h / 2)).toBeNull();
    const before = dropTarget(boxes, sib, parents, a.id, other.x + 2, other.y + 1)!;
    expect(before.mode).toBe("before");
    const sibs = sib(other.parentId).filter((s) => s.id !== a.id);
    const i = sibs.findIndex((s) => s.id === other.id);
    expect(before.order).toBe(orderBetween(sibs[i - 1]?.order ?? "", sibs[i]!.order));
    expect(before.order < sibs[i]!.order).toBe(true);
  });
});
