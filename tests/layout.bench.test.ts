// Layout benchmark: 1,000 nodes, incremental relayout must stay under a frame
// (16 ms). Runs as a test so `make test` enforces the budget; the numbers are
// printed for the PR report.
import { describe, expect, it } from "vitest";
import type { Op } from "../ui/src/contract";
import { applyOp } from "../ui/src/core/doc";
import { buildDisplay } from "../ui/src/canvas/display";
import { TreeLayout } from "../ui/src/layout/tree";
import { ROOT, seededDoc } from "./fixtures";

function median(xs: number[]): number {
  const s = [...xs].sort((a, b) => a - b);
  return s[Math.floor(s.length / 2)]!;
}

describe("layout benchmark (1,000 nodes)", () => {
  it("full and incremental layout stay well under 16 ms", () => {
    const d = seededDoc(1000, 42);
    const full: number[] = [];
    for (let i = 0; i < 15; i++) {
      const t = performance.now();
      new TreeLayout().layout(d.nodes, ROOT);
      full.push(performance.now() - t);
    }
    const L = new TreeLayout();
    L.layout(d.nodes, ROOT);
    const ids = [...d.nodes.keys()].filter((id) => id !== ROOT);
    const inc: number[] = [];
    for (let i = 0; i < 60; i++) {
      // One keystroke-sized change: retitle a node (may change its size), then relayout.
      const id = ids[(i * 37) % ids.length]!;
      const n = d.nodes.get(id)!;
      applyOp(d, {
        id: "op_bench0000001", map_id: "map_test00000001", rev: 10_000 + i, actor: { participant_id: "pt_aaaaaaaaaaaa" },
        type: "node.update", payload: { id, set: { title: `${n.title} x` } },
      } as Op);
      const t = performance.now();
      L.layout(d.nodes, ROOT);
      inc.push(performance.now() - t);
    }
    // The full display pipeline as the canvas runs it on each keystroke (live draft title).
    const disp: number[] = [];
    for (let i = 0; i < 20; i++) {
      const t = performance.now();
      buildDisplay(L, { view: d, proposals: [], runs: [], showGhosts: true, draft: { id: ids[i]!, title: `typing ${i}` } });
      disp.push(performance.now() - t);
    }
    const report = {
      fullMedianMs: median(full), incrementalMedianMs: median(inc), incrementalMaxMs: Math.max(...inc),
      displayMedianMs: median(disp),
    };
    console.log("layout bench (1000 nodes):", JSON.stringify(report, (_, v) => (typeof v === "number" ? Math.round(v * 1000) / 1000 : v)));
    expect(report.incrementalMedianMs).toBeLessThan(16);
    expect(report.displayMedianMs).toBeLessThan(16);
    expect(report.fullMedianMs).toBeLessThan(16);
  });
});
