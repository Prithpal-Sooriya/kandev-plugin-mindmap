// Op reducer + optimistic queue against the fake server: ack, reject, rebase,
// reconnect with resubmission, and the long-poll fallback.
import { afterEach, describe, expect, it } from "vitest";
import { newId, validate, OpShape, type Op } from "../ui/src/contract";
import { applyOp, childrenOf, emptyDoc } from "../ui/src/core/doc";
import type { OpDraft } from "../ui/src/core/sync";
import { FakeServer } from "../ui/src/fake/server";
import { flush, lastSocket, makeClient, type Client } from "./harness";

const create = (parent: string, title: string, order = "V", id = newId("nd")): OpDraft => ({
  type: "node.create", payload: { node: { id, parent_id: parent, order, title, kind: "idea" } },
});
const nodeId = (d: OpDraft) => (d as Extract<OpDraft, { type: "node.create" }>).payload.node.id;

let clients: Client[] = [];
afterEach(() => {
  for (const c of clients) c.conn.stop();
  clients = [];
});

async function setup(n = 2, opts: { failWs?: boolean[] } = {}) {
  const server = new FakeServer();
  const map = server.createMap("Plan a garden");
  const cs = Array.from({ length: n }, (_, i) =>
    makeClient(server, map.id, { name: i === 0 ? undefined : `Guest ${i}`, failWs: opts.failWs?.[i] }),
  );
  clients.push(...cs);
  for (const c of cs) c.conn.start();
  await flush();
  return { server, map, root: map.root_id, cs };
}

describe("op reducer", () => {
  it("applies create/update/move/delete/restore immutably", () => {
    const d = emptyDoc();
    const base = { map_id: "map_aaaaaaaaaaaa", actor: { participant_id: "pt_aaaaaaaaaaaa" } };
    const op = (rev: number, x: OpDraft) => ({ ...base, id: newId("op"), rev, ...x }) as Op;
    applyOp(d, op(1, { type: "node.create", payload: { node: { id: "nd_root00000001", order: "V", title: "Root", kind: "topic" } } }));
    applyOp(d, op(2, create("nd_root00000001", "A", "V", "nd_aaaaaaaaaaa1")));
    applyOp(d, op(3, create("nd_aaaaaaaaaaa1", "B", "V", "nd_bbbbbbbbbbb1")));
    const before = d.nodes.get("nd_aaaaaaaaaaa1");
    applyOp(d, op(4, { type: "node.update", payload: { id: "nd_aaaaaaaaaaa1", set: { title: "A2" }, add_tags: ["x"], append_body: "hi" } }));
    const after = d.nodes.get("nd_aaaaaaaaaaa1")!;
    expect(after).not.toBe(before);
    expect(after).toMatchObject({ title: "A2", tags: ["x"], body_md: "hi", rev: 4 });
    applyOp(d, op(5, { type: "node.delete", payload: { id: "nd_aaaaaaaaaaa1" } }));
    expect(d.nodes.get("nd_bbbbbbbbbbb1")!.deleted).toBe(true);
    expect(childrenOf(d, "nd_root00000001")).toHaveLength(0);
    applyOp(d, op(6, { type: "node.restore", payload: { id: "nd_aaaaaaaaaaa1" } }));
    expect(d.nodes.get("nd_bbbbbbbbbbb1")!.deleted).toBeUndefined();
    applyOp(d, op(7, { type: "node.move", payload: { id: "nd_bbbbbbbbbbb1", parent_id: "nd_root00000001", order: "a" } }));
    expect(childrenOf(d, "nd_root00000001").map((n) => n.title)).toEqual(["A2", "B"]);
  });

  it("produces contract-valid ops from local drafts", async () => {
    const { cs, root } = await setup(1);
    const [op] = cs[0]!.engine.local([create(root, "Valid")]);
    expect(validate(OpShape, op)).toEqual([]);
  });
});

describe("optimistic queue", () => {
  it("shows a local edit at once, then acks it and syncs other clients", async () => {
    const { cs, root, server, map } = await setup(2);
    const [a, b] = cs as [Client, Client];
    const d = create(root, "Compost");
    a.engine.local([d]);
    expect(a.engine.view.nodes.get(nodeId(d))?.title).toBe("Compost"); // same tick: optimistic
    expect(a.engine.pending).toHaveLength(1);
    await flush();
    expect(a.engine.pending).toHaveLength(0);
    expect(a.engine.confirmed.nodes.has(nodeId(d))).toBe(true);
    expect(a.engine.rev).toBe(server.state(map.id).doc.rev);
    expect(b.engine.view.nodes.get(nodeId(d))?.title).toBe("Compost");
    expect(b.engine.trail.has(nodeId(d))).toBe(true); // write trail for others' edits
  });

  it("keeps field-level last-writer-wins edits from two people", async () => {
    const { cs, root } = await setup(2);
    const [a, b] = cs as [Client, Client];
    const d = create(root, "Beds");
    a.engine.local([d]);
    await flush();
    a.engine.local([{ type: "node.update", payload: { id: nodeId(d), set: { title: "Raised beds" } } }]);
    b.engine.local([{ type: "node.update", payload: { id: nodeId(d), set: { body_md: "Cedar, 30 cm" } } }]);
    await flush();
    for (const c of [a, b]) expect(c.engine.view.nodes.get(nodeId(d))).toMatchObject({ title: "Raised beds", body_md: "Cedar, 30 cm" });
  });

  it("rolls back a rejected move (cycle on the server's current tree)", async () => {
    const { cs, root } = await setup(2);
    const [a, b] = cs as [Client, Client];
    const x = create(root, "X", "V");
    const y = create(root, "Y", "a");
    a.engine.local([x, y]);
    await flush();
    // Concurrently: A moves X under Y, B moves Y under X. Both are legal locally.
    a.engine.local([{ type: "node.move", payload: { id: nodeId(x), parent_id: nodeId(y), order: "V" } }]);
    b.engine.local([{ type: "node.move", payload: { id: nodeId(y), parent_id: nodeId(x), order: "V" } }]);
    expect(b.engine.view.nodes.get(nodeId(y))!.parent_id).toBe(nodeId(x));
    await flush();
    expect(b.errors).toEqual(["cycle"]);
    expect(b.engine.pending).toHaveLength(0);
    for (const c of [a, b]) {
      expect(c.engine.view.nodes.get(nodeId(x))!.parent_id).toBe(nodeId(y));
      expect(c.engine.view.nodes.get(nodeId(y))!.parent_id).toBe(root);
    }
  });

  it("rejects the whole batch atomically", async () => {
    const { cs, root } = await setup(1);
    const a = cs[0]!;
    a.engine.local([create(root, "ok"), { type: "node.update", payload: { id: "nd_missing000001", set: { title: "?" } } }]);
    expect(childrenOf(a.engine.view, root)).toHaveLength(1);
    await flush();
    expect(a.errors).toEqual(["not_found"]);
    expect(childrenOf(a.engine.view, root)).toHaveLength(0);
  });

  it("rejects writes to a soft-locked field", async () => {
    const { cs, root, server, map } = await setup(2);
    const [a, b] = cs as [Client, Client];
    const d = create(root, "Shed");
    a.engine.local([d]);
    await flush();
    server.setPresence(map.id, {
      participant_id: b.engine.participant!.id, updated_at: server.now(),
      editing: { node_id: nodeId(d), field: "title", expires_at: new Date(Date.now() + 15000).toISOString() },
    });
    a.engine.local([{ type: "node.update", payload: { id: nodeId(d), set: { title: "Barn" } } }]);
    a.engine.local([{ type: "node.update", payload: { id: nodeId(d), append_body: "merge-style still allowed" } }]);
    await flush();
    expect(a.errors).toEqual(["locked"]);
    expect(a.engine.view.nodes.get(nodeId(d))).toMatchObject({ title: "Shed", body_md: "merge-style still allowed" });
  });

  it("applies a rebase when two inserts pick the same order key", async () => {
    const { cs, root } = await setup(2);
    const [a, b] = cs as [Client, Client];
    const p = create(root, "Path", "V");
    const q = create(root, "Pond", "V");
    a.engine.local([p]);
    b.engine.local([q]);
    await flush();
    const orders = [a, b].map((c) => childrenOf(c.engine.view, root).map((n) => `${n.title}@${n.order}`));
    expect(orders[0]).toEqual(orders[1]);
    const [k1, k2] = childrenOf(a.engine.view, root).map((n) => n.order);
    expect(k1).not.toBe(k2);
    expect(b.engine.pending).toHaveLength(0);
  });
});

describe("reconnect", () => {
  it("queues edits while offline and resubmits them after hello since_rev", async () => {
    const { cs, root, server, map } = await setup(2);
    const [a, b] = cs as [Client, Client];
    b.engine.local([create(root, "before")]);
    await flush();
    const revBefore = a.engine.rev;
    lastSocket(a).close();
    await flush(1);
    const d = create(root, "offline edit");
    a.engine.local([d]);
    b.engine.local([create(root, "while A was away")]);
    await flush(12);
    expect(a.sockets.length).toBeGreaterThan(1);
    expect(a.engine.pending).toHaveLength(0);
    expect(a.engine.rev).toBe(server.state(map.id).doc.rev);
    expect(a.engine.rev).toBeGreaterThan(revBefore);
    expect(childrenOf(a.engine.view, root).map((n) => n.title).sort()).toEqual(["before", "offline edit", "while A was away"]);
    expect(childrenOf(b.engine.view, root)).toHaveLength(3);
  });

  it("does not apply an op twice when its ack was lost (idempotent op ids)", async () => {
    const { cs, root, server, map } = await setup(1);
    const a = cs[0]!;
    lastSocket(a).blackhole = true; // the server commits, the ack never arrives
    const d: OpDraft = { type: "node.update", payload: { id: root, append_body: "once" } };
    a.engine.local([d]);
    await flush();
    expect(a.engine.pending).toHaveLength(1);
    const logLen = server.state(map.id).log.length;
    lastSocket(a).close();
    await flush(12);
    expect(server.state(map.id).log.length).toBe(logLen);
    expect(a.engine.pending).toHaveLength(0);
    expect(a.engine.view.nodes.get(root)!.body_md).toBe("once");
  });

  it("gets a snapshot on first hello and catch-up ops on resume", async () => {
    const { cs } = await setup(1);
    const a = cs[0]!;
    expect(a.engine.hasSnapshot).toBe(true);
    expect(a.engine.resumeFields().since_rev).toBe(a.engine.rev);
  });
});

describe("server undo (protocol §13)", () => {
  it("reverts the caller's latest batch, broadcasts it to everyone, and leaves others' edits alone", async () => {
    const { cs, root, server, map } = await setup(2);
    const [a, b] = cs as [Client, Client];
    const mine = create(root, "Mine");
    a.engine.local([mine]);
    await flush();
    const theirs = create(root, "Theirs");
    b.engine.local([theirs]);
    await flush();
    const res = server.undo(map.id, a.engine.participant!.id);
    expect(res.ops.map((o) => o.type)).toEqual(["node.delete"]);
    await flush();
    for (const c of [a, b]) expect(childrenOf(c.engine.view, root).map((n) => n.title)).toEqual(["Theirs"]);
    // Nothing left to undo for A; the undo itself is not undoable.
    expect(() => server.undo(map.id, a.engine.participant!.id)).toThrow(/nothing to undo/);
  });
});

describe("transport fallback", () => {
  it("falls back to long-poll when the WebSocket cannot open, and still syncs", async () => {
    const { cs, root } = await setup(2, { failWs: [true, false] });
    const [a, b] = cs as [Client, Client];
    await flush(10);
    expect(a.conn.state).toBe("longpoll");
    expect(a.engine.hasSnapshot).toBe(true);
    const d = create(root, "via long-poll");
    a.engine.local([d]);
    await flush(10);
    expect(a.submitCalls).toBeGreaterThan(0);
    expect(a.engine.pending).toHaveLength(0);
    expect(b.engine.view.nodes.get(nodeId(d))?.title).toBe("via long-poll");
    const e = create(root, "from the socket client");
    b.engine.local([e]);
    await flush(20);
    expect(a.engine.view.nodes.get(nodeId(e))?.title).toBe("from the socket client");
  });

  it("returns to the WebSocket when it becomes reachable", async () => {
    const { cs } = await setup(1, { failWs: [true] });
    const a = cs[0]!;
    await flush(10);
    expect(a.conn.state).toBe("longpoll");
    a.failWs = false;
    (a.conn as unknown as { openWs(): Promise<void> }).openWs();
    await flush(10);
    expect(a.conn.state).toBe("ws");
  });
});

describe("proposals", () => {
  it("reviews items by op id; rejected creates make dependants conflict", async () => {
    const { server, map, root, cs } = await setup(1);
    const agent = server.addParticipant(map.id, { kind: "agent", name: "Gardener", harness: "claude", run_id: "run_aaaaaaaaaaaa" });
    const p1 = create(root, "Ghost parent");
    const p2 = create(nodeId(p1), "Ghost child");
    const prop = server.propose(map.id, "run_aaaaaaaaaaaa", agent.id, [p1, p2]);
    await flush();
    expect(cs[0]!.engine.proposals.get(prop.id)?.items).toHaveLength(2);
    const res = server.review(prop.id, { reject: [prop.items[0]!.op.id], accept: [prop.items[1]!.op.id] }, "pt_x");
    expect(res.items.map((i) => i.status)).toEqual(["rejected", "conflict"]);
    expect(res.status).toBe("rejected");
  });
});
