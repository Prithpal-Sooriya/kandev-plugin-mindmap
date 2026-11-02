// Cleanup + free-layout + mouse-control units: layout position overrides,
// trimmed link endpoints, and the fake server's archive/delete behaviors.
import { describe, expect, it } from "vitest";
import { FakeServer } from "../ui/src/fake/server";
import { linkPath } from "../ui/src/layout/geometry";
import { TreeLayout } from "../ui/src/layout/tree";
import { flush, makeClient } from "./harness";
import { ROOT, seededDoc } from "./fixtures";

describe("layout position overrides", () => {
  it("pins the node at the override and carries its subtree", () => {
    const d = seededDoc(40, 3);
    const L = new TreeLayout();
    const auto = L.layout(d.nodes, ROOT);
    const branch = [...auto.boxes.values()].find((b) => b.depth === 1 && (auto.children.get(b.id)?.length ?? 0) > 0)!;
    const moved = L.layout(d.nodes, ROOT, { pos: (id) => (id === branch.id ? { x: 900, y: -700 } : undefined) });
    expect(moved.boxes.get(branch.id)).toMatchObject({ x: 900, y: -700 });
    // Children stack to the right of the pinned card, not the auto spot.
    const child = moved.boxes.get(moved.children.get(branch.id)![0]!)!;
    expect(child.x).toBeGreaterThan(900 + branch.w - 1);
    // The child's center is within one card height of the pinned card's center.
    expect(Math.abs(child.y + child.h / 2 - (-700 + branch.h / 2))).toBeLessThan(child.h + 40);
    // Unrelated branches are untouched.
    const other = [...auto.boxes.values()].find((b) => b.depth === 1 && b.id !== branch.id)!;
    expect(moved.boxes.get(other.id)).toBe(auto.boxes.get(other.id));
  });

  it("a root override moves the whole map", () => {
    const d = seededDoc(40, 3);
    const auto = new TreeLayout().layout(d.nodes, ROOT);
    const root = auto.boxes.get(ROOT)!;
    const shifted = new TreeLayout().layout(d.nodes, ROOT, { pos: (id) => (id === ROOT ? { x: 1000, y: 800 } : undefined) });
    const first = shifted.boxes.get([...shifted.children.get(ROOT)!].find((c) => shifted.boxes.get(c)!.side === 1)!)!;
    const autoFirst = auto.boxes.get([...auto.children.get(ROOT)!].find((c) => auto.boxes.get(c)!.side === 1)!)!;
    expect(first.x - autoFirst.x).toBeCloseTo(1000 - (-root.w / 2), 0);
    expect(first.y - autoFirst.y).toBeCloseTo(800 - (-root.h / 2), 0);
  });

  it("no overrides reproduces the automatic layout exactly", () => {
    const d = seededDoc(40, 3);
    const L = new TreeLayout();
    const a = L.layout(d.nodes, ROOT);
    const b = L.layout(d.nodes, ROOT, { pos: () => undefined });
    for (const id of a.boxes.keys()) expect(b.boxes.get(id)).toBe(a.boxes.get(id));
  });
});

describe("link endpoints", () => {
  it("trims both ends to the card edges", () => {
    const d = seededDoc(20, 2);
    const boxes = new TreeLayout().layout(d.nodes, ROOT).boxes;
    const a = boxes.get(ROOT)!;
    const b = [...boxes.values()].find((x) => x.depth === 1)!;
    const lp = linkPath(a, b);
    const inside = (box: typeof a, x: number, y: number) => x >= box.x - 0.5 && x <= box.x + box.w + 0.5 && y >= box.y - 0.5 && y <= box.y + box.h + 0.5;
    expect(inside(a, lp.sx, lp.sy)).toBe(true);
    expect(inside(b, lp.ex, lp.ey)).toBe(true);
    // Trimming actually happened: the ends are not the raw card centers.
    expect(lp.sx).not.toBe(a.x + a.w / 2);
    expect(lp.ex).not.toBe(b.x + b.w / 2);
    expect(lp.d.startsWith("M")).toBe(true);
  });
});

describe("map cleanup (fake server)", () => {
  it("archive splits the list and unarchive restores, through the real submit loop", async () => {
    const server = new FakeServer();
    const a = server.createMap("Active");
    server.createMap("To archive");
    const b = server.listMaps().find((m) => m.title === "To archive")!;
    expect(server.listMaps()).toHaveLength(2);

    const client = makeClient(server, b.id);
    client.conn.start();
    await flush();
    client.engine.local([{ type: "map.update", payload: { settings: { archived: true } } }]);
    await flush();

    expect(server.listMaps(false).map((m) => m.title)).toEqual(["Active"]);
    const archived = server.listMaps(true);
    expect(archived).toHaveLength(1);
    expect(archived[0]!.archived).toBe(true);
    expect(archived[0]!.title).toBe("To archive");
    expect(a.id).toBe("map_" + a.id.slice(4)); // sanity: ids stay prefixed

    // Unarchive via the same op path.
    client.engine.local([{ type: "map.update", payload: { settings: { archived: false } } }]);
    await flush();
    expect(server.listMaps(false)).toHaveLength(2);
    expect(server.listMaps(true)).toHaveLength(0);
  });

  it("layout settings ride the same op and land on the map", async () => {
    const server = new FakeServer();
    const m = server.createMap("Movable");
    const client = makeClient(server, m.id);
    client.conn.start();
    await flush();
    const nodeId = server.snapshot(m.id).nodes[0]!.id;
    client.engine.local([{ type: "map.update", payload: { settings: { layout: { [nodeId]: { x: 12, y: -30 } } } } }]);
    await flush();
    expect(server.snapshot(m.id).map.settings.layout?.[nodeId]).toEqual({ x: 12, y: -30 });
  });

  it("deleteMap removes the map but refuses while agents run", () => {
    const server = new FakeServer();
    const m = server.createMap("Doomed");
    server.deleteMap(m.id);
    expect(server.listMaps(false)).toHaveLength(0);
    expect(server.listMaps(true)).toHaveLength(0);
    expect(() => server.state(m.id)).toThrow();

    const busy = server.createMap("Busy");
    server.state(busy.id).runs.set("run_busy", { id: "run_busy", status: "running" } as never);
    expect(() => server.deleteMap(busy.id)).toThrow(/live agents/);
    expect(server.listMaps(false).map((x) => x.title)).toContain("Busy");
  });
});
