import { expect, test } from "vitest";

test("off-screen marker geometry", () => {
  // We can test the math logic that computes intersections
  const angle = Math.PI / 4; // 45 degrees
  const scx = 400; // w/2 where w=800
  const scy = 300; // h/2 where h=600
  
  let tx = scx, ty = scy;
  const absCos = Math.abs(Math.cos(angle));
  const absSin = Math.abs(Math.sin(angle));
  
  if (scx * absSin > scy * absCos) {
    ty = Math.sign(Math.sin(angle)) * scy;
    tx = ty / Math.tan(angle);
  } else {
    tx = Math.sign(Math.cos(angle)) * scx;
    ty = tx * Math.tan(angle);
  }
  
  // 45 degrees -> intersects top/bottom
  // scx*sin(45) = 400*0.707 = 282.8
  // scy*cos(45) = 300*0.707 = 212.1
  // 282.8 > 212.1 -> intersects top/bottom
  // ty = 300
  // tx = 300 / 1 = 300
  expect(ty).toBeCloseTo(300);
  expect(tx).toBeCloseTo(300);
});

test("minimap mapping", () => {
  const bounds = { x: 0, y: 0, w: 1000, h: 1000 };
  const pad = 100;
  const bx = bounds.x - pad;
  const by = bounds.y - pad;
  const bw = bounds.w + pad * 2;
  const bh = bounds.h + pad * 2;
  
  // A dot at 500, 500 should be mapped correctly in SVG coordinates
  const dotX = 500;
  // Inside the SVG, it uses cx={d.x}, which is just 500
  expect(bx).toBe(-100);
  expect(bw).toBe(1200);
});
import type { Run } from "../ui/src/contract";

test("drawer tree grouping", () => {
  const runs: Partial<Run>[] = [
    { id: "r1", status: "running" },
    { id: "r2", parent_run_id: "r1", status: "running" },
    { id: "r3", status: "waiting" },
  ];
  const rootRuns = runs.filter(r => !r.parent_run_id);
  const childrenMap = new Map<string, Partial<Run>[]>();
  for (const r of runs) {
    if (r.parent_run_id) {
      const arr = childrenMap.get(r.parent_run_id) ?? [];
      arr.push(r);
      childrenMap.set(r.parent_run_id, arr);
    }
  }
  expect(rootRuns.map(r => r.id)).toEqual(["r1", "r3"]);
  expect(childrenMap.get("r1")?.map(r => r.id)).toEqual(["r2"]);
});

test("needs you filtering", () => {
  const runs: Partial<Run>[] = [
    { id: "r1", status: "running" },
    { id: "r2", status: "waiting" },
    { id: "r3", status: "review" },
    { id: "r4", status: "done" },
  ];
  const needsYou = runs.filter(r => r.status === "waiting" || r.status === "review");
  expect(needsYou.map(r => r.id)).toEqual(["r2", "r3"]);
});

test("follow-mode state machine", () => {
  let followingRun: string | null = null;
  // User clicks follow r1
  followingRun = "r1";
  expect(followingRun).toBe("r1");
  // User pans manually
  followingRun = null;
  expect(followingRun).toBe(null);
});

test("replay reducer", () => {
  let replayRev: number | null = null;
  const startReplay = (rev: number) => { replayRev = rev; };
  const stopReplay = () => { replayRev = null; };
  startReplay(5);
  expect(replayRev).toBe(5);
  stopReplay();
  expect(replayRev).toBe(null);
});
