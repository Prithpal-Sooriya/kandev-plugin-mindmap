// Dev server for the standalone app against the in-memory FakeServer: serves
// the dev build at /m/<token>/, implements the loopback routes of protocol §4
// (WebSocket with a hand-rolled RFC 6455 framer, long-poll sync, ops, maps,
// proposals, assets), and runs a few simulated participants so presence,
// agent rings, write trails and ghost proposals can be seen.
//
//   npm run dev            → http://127.0.0.1:5178/  (local token: map list)
//   /m/<share token>/      → guest view of the demo map
//   ?seed=1000             → creates and opens a 1,000-node map (perf)
//   ?ws=0                  → refuse WebSocket upgrades (exercise the long-poll fallback)
//   /kandev/               → mock Kandev host running the real in-Kandev bundle (ui/bundle.js)
//                            through mock session/sync/submit/api/asset webhooks
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import type { Duplex } from "node:stream";
import { join } from "node:path";
import { newId, orderBetween, type Asset, type NodeKind, type ClientMessage, type Run, type ServerMessage } from "../src/contract";
import type { OpDraft } from "../src/core/sync";
import { FakeServer, FakeReject, type FakeConn } from "../src/fake/server";

const PORT = Number(process.env.PORT ?? 5178);
const WEB = process.env.MM_WEB ?? ".build/dev-web";
const LOCAL = `mml1.${"L".repeat(43)}`;
const SHARE = `mms1.${"S".repeat(43)}`;
const BOTS = !process.argv.includes("--no-bots");

const server = new FakeServer();
const demo = buildDemo();
let noWs = false;

// --- HTTP ------------------------------------------------------------------------------

function send(res: ServerResponse, status: number, body?: unknown, type = "application/json") {
  res.writeHead(status, { "Content-Type": type, "Cache-Control": "no-store" });
  res.end(body === undefined ? "" : typeof body === "string" || Buffer.isBuffer(body) ? body : JSON.stringify(body));
}

async function readBody(req: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = [];
  for await (const c of req) chunks.push(c as Buffer);
  const s = Buffer.concat(chunks).toString("utf8");
  return s ? JSON.parse(s) : undefined;
}

function scope(token: string): { kind: "local" | "share"; maps: string[] | null } | null {
  if (token === LOCAL) return { kind: "local", maps: null };
  if (token === SHARE) return { kind: "share", maps: [demo.mapId] };
  return null;
}

const shareGuest = new Map<string, string>();
function participantFor(kind: "local" | "share", mapId: string): string {
  if (kind === "local") return server.hostOf(mapId).id;
  let id = shareGuest.get(mapId);
  if (!id) shareGuest.set(mapId, (id = server.addParticipant(mapId, { name: "Guest (long-poll)" }).id));
  return id;
}

const KANDEV_HTML = `<!doctype html><html><head><meta charset="utf-8"><title>Mock Kandev host</title>
<link rel="stylesheet" href="/kandev/app.css"></head><body><div id="root"></div><script type="module" src="/kandev/host.js"></script></body></html>`;

/** Mock Kandev webhooks (protocol §3), backed by the same FakeServer. */
async function webhook(key: string, req: IncomingMessage, res: ServerResponse, url: URL) {
  const mapId = url.searchParams.get("map_id") ?? "";
  if (key === "session") {
    await readBody(req);
    return send(res, 200, {
      contract: 1, base_url: `http://127.0.0.1:${PORT}`, token: LOCAL, expires_at: new Date(Date.now() + 12 * 3600e3).toISOString(),
      participant: { ...server.hostOf(demo.mapId), id: "" },
    });
  }
  if (!server.maps.has(mapId) && key !== "api") return send(res, 404, { error: { code: "not_found", message: "no such map" } });
  const pid = () => server.hostOf(mapId).id;
  if (key === "sync") {
    const wait = Math.min(Number(url.searchParams.get("wait") ?? 0), 20) * 1000;
    return send(res, 200, await server.sync(mapId, pid(), url.searchParams.get("cursor") || undefined, wait, url.searchParams.get("client_id") ?? undefined));
  }
  if (key === "submit") return send(res, 200, server.submitHttp(mapId, pid(), (await readBody(req)) as never));
  if (key === "asset") return send(res, 200, demoImage(url.searchParams.get("id") ?? ""), "image/svg+xml");
  if (key === "api") {
    const body = (await readBody(req)) as { method: string; path: string; body?: unknown };
    const r = await fetch(`http://127.0.0.1:${PORT}/m/${LOCAL}${body.path}`, {
      method: body.method, headers: { "Content-Type": "application/json" }, ...(body.body !== undefined ? { body: JSON.stringify(body.body) } : {}),
    });
    return send(res, r.status, await r.text());
  }
  return send(res, 404, "");
}

const httpServer = createServer(async (req, res) => {
  try {
    const url = new URL(req.url ?? "/", `http://${req.headers.host}`);
    if (url.searchParams.get("ws") === "0") noWs = true;
    if (url.searchParams.get("ws") === "1") noWs = false;
    if (url.pathname === "/kandev/" || url.pathname === "/mindmap") return send(res, 200, KANDEV_HTML, "text/html; charset=utf-8");
    if (url.pathname === "/kandev/host.js") return send(res, 200, readFileSync(".build/kandev-host.js"), "text/javascript; charset=utf-8");
    if (url.pathname === "/kandev/app.css") return send(res, 200, readFileSync(join(WEB, "app.css")), "text/css; charset=utf-8");
    if (url.pathname === "/ui/bundle.js") return send(res, 200, readFileSync("ui/bundle.js"), "text/javascript; charset=utf-8");
    const wh = /^\/api\/plugins\/kandev-plugin-mindmap\/webhooks\/([a-z]+)$/.exec(url.pathname);
    if (wh) return webhook(wh[1]!, req, res, url);
    if (url.pathname === "/" || url.pathname === "/index.html") {
      res.writeHead(302, { Location: `/m/${LOCAL}/${url.search}` });
      return res.end();
    }
    const m = /^\/m\/([^/]+)(\/.*)?$/.exec(url.pathname);
    const sc = m ? scope(decodeURIComponent(m[1]!)) : null;
    if (!m || !sc) return send(res, 404, "");
    const rest = m[2] ?? "/";
    if (rest === "/" || rest === "/index.html") return send(res, 200, readFileSync(join(WEB, "index.html")), "text/html; charset=utf-8");
    if (rest === "/app.js") return send(res, 200, readFileSync(join(WEB, "app.js")), "text/javascript; charset=utf-8");
    if (rest === "/app.css") return send(res, 200, readFileSync(join(WEB, "app.css")), "text/css; charset=utf-8");
    const asset = /^\/assets\/([^/]+)$/.exec(rest);
    if (asset) return send(res, 200, demoImage(asset[1]!), "image/svg+xml");
    const api = /^\/api(\/.*)$/.exec(rest);
    if (!api) return send(res, 404, "");
    const path = api[1]!;
    const method = req.method ?? "GET";
    const allowed = (mapId: string) => !sc.maps || sc.maps.includes(mapId);
    let r: RegExpExecArray | null;
    if (path === "/maps" && method === "GET") {
      return send(res, 200, server.listMaps().filter((x) => allowed(x.id)));
    }
    if (path === "/maps" && method === "POST") {
      if (sc.kind !== "local") return send(res, 403, { error: { code: "forbidden", message: "share links cannot create maps" } });
      const body = (await readBody(req)) as { title: string; workspace_id: string };
      return send(res, 200, server.createMap(body.title, body.workspace_id));
    }
    if (path === "/profiles") {
      return send(res, 200, [{ id: "profile-claude", name: "Claude", harness: "claude" }, { id: "profile-cursor", name: "Cursor", harness: "cursor" }]);
    }
    if (path === "/repos") return send(res, 200, [{ id: "repo-demo", name: "example-app" }]);
    if ((r = /^\/maps\/([^/]+)\/(snapshot|sync|ops|undo|runs|swarms|compile|suggest)$/.exec(path))) {
      const mapId = r[1]!;
      if (!allowed(mapId) || !server.maps.has(mapId)) return send(res, 404, sc.kind === "share" ? "" : { error: { code: "not_found", message: "no such map" } });
      const pid = participantFor(sc.kind, mapId);
      switch (r[2]) {
        case "snapshot":
          return send(res, 200, server.snapshot(mapId));
        case "sync": {
          const wait = Math.min(Number(url.searchParams.get("wait") ?? 0), 20) * 1000;
          return send(res, 200, await server.sync(mapId, pid, url.searchParams.get("cursor") || undefined, wait, url.searchParams.get("client_id") ?? undefined));
        }
        case "ops":
          return send(res, 200, server.submitHttp(mapId, pid, (await readBody(req)) as never));
        case "undo":
          return send(res, 200, server.undo(mapId, pid));
        default:
          return send(res, 501, { error: { code: "not_implemented", message: "agent runs arrive in Phase 2" } });
      }
    }
    if ((r = /^\/proposals\/([^/]+)\/review$/.exec(path)) && method === "POST") {
      const body = (await readBody(req)) as never;
      return send(res, 200, server.review(r[1]!, body, server.hostOf(demo.mapId).id));
    }
    return send(res, 404, { error: { code: "not_found", message: `no route ${method} ${path}` } });
  } catch (e) {
    if (e instanceof FakeReject) return send(res, e.code === "not_found" ? 404 : 409, { error: { code: e.code, message: e.message } });
    console.error(e);
    return send(res, 500, { error: { code: "internal", message: String(e) } });
  }
});

// --- WebSocket (RFC 6455, text frames only) ---------------------------------------------------

httpServer.on("upgrade", (req: IncomingMessage, socket: Duplex) => {
  const url = new URL(req.url ?? "/", `http://${req.headers.host}`);
  const m = /^\/m\/([^/]+)\/ws$/.exec(url.pathname);
  const sc = m ? scope(decodeURIComponent(m[1]!)) : null;
  const mapId = url.searchParams.get("map_id") ?? "";
  if (noWs || !sc || !server.maps.has(mapId) || (sc.maps && !sc.maps.includes(mapId))) {
    socket.end("HTTP/1.1 404 Not Found\r\n\r\n");
    return;
  }
  const accept = createHash("sha1").update(`${req.headers["sec-websocket-key"]}258EAFA5-E914-47DA-95CA-C5AB0DC85B11`).digest("base64");
  socket.write(`HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${accept}\r\n\r\n`);
  const conn: FakeConn = server.connect(mapId, (msg: ServerMessage) => writeFrame(socket, 1, Buffer.from(JSON.stringify(msg))));
  let buf = Buffer.alloc(0);
  socket.on("data", (chunk: Buffer) => {
    buf = Buffer.concat([buf, chunk]);
    for (;;) {
      const f = readFrame(buf);
      if (!f) break;
      buf = buf.subarray(f.size);
      if (f.op === 8) {
        writeFrame(socket, 8, Buffer.alloc(0));
        socket.end();
        return;
      }
      if (f.op === 9) writeFrame(socket, 10, f.data);
      if (f.op !== 1) continue;
      try {
        const msg = JSON.parse(f.data.toString("utf8")) as ClientMessage;
        // Share-token hellos create a guest; local hellos are the host.
        if (msg.type === "hello" && sc.kind === "local") delete (msg.payload as { name?: string }).name;
        server.receive(conn, msg);
      } catch (e) {
        writeFrame(socket, 1, Buffer.from(JSON.stringify({ type: "error", payload: { error: { code: "bad_request", message: String(e) } } })));
      }
    }
  });
  const close = () => server.disconnect(conn);
  socket.on("close", close);
  socket.on("error", close);
});

function readFrame(b: Buffer): { op: number; data: Buffer; size: number } | null {
  if (b.length < 2) return null;
  const op = b[0]! & 0x0f;
  const masked = (b[1]! & 0x80) !== 0;
  let len = b[1]! & 0x7f;
  let off = 2;
  if (len === 126) {
    if (b.length < 4) return null;
    len = b.readUInt16BE(2);
    off = 4;
  } else if (len === 127) {
    if (b.length < 10) return null;
    len = Number(b.readBigUInt64BE(2));
    off = 10;
  }
  const maskOff = off;
  if (masked) off += 4;
  if (b.length < off + len) return null;
  const data = Buffer.from(b.subarray(off, off + len));
  if (masked) for (let i = 0; i < len; i++) data[i]! ^= b[maskOff + (i % 4)]!;
  return { op, data, size: off + len };
}

function writeFrame(socket: Duplex, op: number, data: Buffer) {
  if (socket.destroyed) return;
  const len = data.length;
  const head = len < 126 ? Buffer.from([0x80 | op, len]) : len < 65536 ? Buffer.alloc(4) : Buffer.alloc(10);
  if (len >= 126 && len < 65536) {
    head[0] = 0x80 | op;
    head[1] = 126;
    head.writeUInt16BE(len, 2);
  } else if (len >= 65536) {
    head[0] = 0x80 | op;
    head[1] = 127;
    head.writeBigUInt64BE(BigInt(len), 2);
  }
  socket.write(Buffer.concat([head, data]));
}

// --- demo map (synthetic) -----------------------------------------------------------------

function demoImage(id: string): string {
  const hue = [...id].reduce((a, c) => a + c.charCodeAt(0), 0) % 360;
  return `<svg xmlns="http://www.w3.org/2000/svg" width="320" height="200" viewBox="0 0 320 200"><rect width="320" height="200" fill="hsl(${hue} 60% 85%)"/><rect x="20" y="20" width="280" height="24" rx="4" fill="hsl(${hue} 50% 60%)"/><rect x="20" y="60" width="130" height="110" rx="6" fill="hsl(${hue} 40% 70%)"/><rect x="170" y="60" width="130" height="50" rx="6" fill="hsl(${hue} 40% 70%)"/><rect x="170" y="120" width="130" height="50" rx="6" fill="hsl(${hue} 40% 70%)"/></svg>`;
}

function buildDemo() {
  const map = server.createMap("Plan a community garden", "ws-demo");
  const host = server.hostOf(map.id).id;
  const ids: Record<string, string> = {};
  const drafts: OpDraft[] = [];
  const last: Record<string, string> = {};
  const add = (key: string, parent: string, title: string, kind: NodeKind = "idea", extra: Record<string, unknown> = {}) => {
    const id = newId("nd");
    ids[key] = id;
    const p = parent === "root" ? map.root_id : ids[parent]!;
    const order = orderBetween(last[p] ?? "", "");
    last[p] = order;
    drafts.push({ type: "node.create", payload: { node: { id, parent_id: p, order, title, kind, ...extra } } });
  };
  add("site", "root", "Site & layout");
  add("site1", "site", "South-facing plot behind the library", "decision");
  add("site2", "site", "Wheelchair-accessible paths", "action");
  add("site3", "site", "Shade from the oak tree", "risk", { confidence: 0.4 });
  add("soil", "root", "Soil & compost", "research", { sources: [{ url: "https://example.com/soil-guide", title: "Example soil guide" }], confidence: 0.7 });
  add("soil1", "soil", "Test pH before planting", "action");
  add("soil2", "soil", "Three-bin compost system", "example", { sources: [{ url: "https://example.org/compost", title: "Example compost how-to", quote: "Turn the pile every two weeks." }], tags: ["compost"] });
  add("water", "root", "Water", "idea");
  add("water1", "water", "Rainwater butts on the shed", "idea", { tags: ["low-cost"] });
  add("water2", "water", "Drip irrigation on a timer", "research");
  add("water3", "water", "Who has the tap key?", "question");
  add("people", "root", "People & rota");
  add("people1", "people", "Weekend volunteer rota", "action");
  add("people2", "people", "Partner with the primary school", "idea");
  add("people3", "people", "Insurance for volunteers", "risk");
  add("money", "root", "Budget", "idea");
  add("money1", "money", "Seed fund from the council grant", "research", { confidence: 0.85, sources: [{ url: "https://example.gov/grants", title: "Example grant page" }] });
  add("money2", "money", "Tool library donations", "idea");
  add("money3", "money", "Plot rental fees?", "decision", { collapsed: true });
  add("money31", "money3", "Free for under-18s", "idea");
  add("money32", "money3", "Sliding scale", "idea");
  add("plants", "root", "What to grow", "idea");
  add("plants1", "plants", "Native pollinator strip", "idea", { tags: ["bees", "native"] });
  add("plants2", "plants", "Herb spiral", "example");
  add("plants3", "plants", "Fruit trees along the fence", "idea");
  server.commitAs(map.id, host, drafts, {}, false);
  server.commitAs(map.id, host, [
    { type: "link.create", payload: { link: { id: newId("ln"), from: ids.water1!, to: ids.plants1!, label: "irrigates" } } },
    { type: "link.create", payload: { link: { id: newId("ln"), from: ids.money2!, to: ids.people1!, label: "needs" } } },
    { type: "node.update", payload: { id: ids.site1!, set: { body_md: "## Why here\n\n- Gets **6+ hours** of sun\n- Close to a tap\n- Visible from the street, which helps safety\n\nSee [the example plan](https://example.com/plan)." } } },
  ], {}, false);
  const asset: Asset = {
    id: newId("as"), node_id: ids.plants2!, kind: "image", mime: "image/svg+xml", bytes: 900, sha256: "a".repeat(64),
    filename: "herb-spiral.svg", caption: "Sketch of a herb spiral", author: { participant_id: host }, created_at: server.now(), width: 320, height: 200,
  };
  server.commitAs(map.id, host, [{ type: "asset.attach", payload: { asset } }], {}, false);
  return { mapId: map.id, ids, host };
}

function runFor(p: { id: string; run_id?: string }, nodeId: string, harness: Run["harness"], status: Run["status"], extra: Partial<Run> = {}): Run {
  const ts = server.now();
  return {
    id: p.run_id!, map_id: demo.mapId, node_id: nodeId, profile_id: `profile-${harness}`, profile_name: harness === "claude" ? "Claude" : "Cursor",
    harness, template: "research", mode: "apply", budget: { max_nodes: 12 }, usage: { nodes: 0, subagents: 0, depth: 0 },
    status, transport: "mcp", participant_id: p.id, started_by: demo.host, created_at: ts, updated_at: ts, ...extra,
  };
}

function startBots() {
  const mapId = demo.mapId;
  const ids = demo.ids;
  const ada = server.addParticipant(mapId, { name: "Ada", color: "#ec4899" });
  const researcher = server.addParticipant(mapId, { kind: "agent", name: "Researcher (Claude)", color: "#3b82f6", harness: "claude", run_id: newId("run") });
  const skeptic = server.addParticipant(mapId, { kind: "agent", name: "Skeptic (Cursor)", color: "#f59e0b", harness: "cursor", run_id: newId("run") });
  const gardener = server.addParticipant(mapId, { kind: "agent", name: "Gardener (Claude)", color: "#a855f7", harness: "claude", run_id: newId("run") });
  const queued = server.addParticipant(mapId, { kind: "agent", name: "Builder (pi)", color: "#22c55e", harness: "pi", run_id: newId("run") });
  server.setRun(runFor(researcher, ids.soil!, "claude", "running", { lens: "researcher", last_output: "Reading soil guides…" }));
  server.setRun(runFor(skeptic, ids.water3!, "cursor", "waiting", { lens: "skeptic", waiting_reason: "question" }));
  server.setRun(runFor(queued, ids.people2!, "other", "queued", { lens: "builder" }));
  server.setRun(runFor(gardener, ids.plants!, "claude", "review", { template: "garden", mode: "propose" }));

  // Ada: a cursor drifting around the "Water" branch, selecting a node, soft-locking a title now and then.
  let t = 0;
  setInterval(() => {
    t += 0.08;
    server.setPresence(mapId, {
      participant_id: ada.id, updated_at: server.now(), cursor: { x: 330 + Math.cos(t) * 90, y: -40 + Math.sin(t * 1.3) * 60 },
      selection: [ids.water1!],
      ...(Math.sin(t / 3) > 0.3 ? { editing: { node_id: ids.people1!, field: "title" as const, expires_at: new Date(Date.now() + 15000).toISOString() } } : {}),
    });
  }, 120);

  // Researcher: adds cited findings under "Soil & compost", then reports.
  const finds = ["Clay soil drains slowly", "Leaf mould as a free amendment", "Municipal compost pick-up", "Raised beds avoid contamination", "Worm bins for kitchen scraps", "No-dig method trial"];
  let n = 0;
  setInterval(() => {
    if (n >= finds.length) {
      n = 0;
      server.setRun(runFor(researcher, ids.soil!, "claude", "done", { summary: "Added soil findings" }));
      setTimeout(() => server.setRun(runFor(researcher, ids.soil!, "claude", "running", { lens: "researcher" })), 4000);
      return;
    }
    const kids = [...server.state(mapId).doc.nodes.values()].filter((x) => x.parent_id === ids.soil && !x.deleted).sort((a, b) => (a.order < b.order ? -1 : 1));
    const title = finds[n++]!;
    server.commitAs(mapId, researcher.id, [{
      type: "node.create",
      payload: { node: { id: newId("nd"), parent_id: ids.soil!, order: orderBetween(kids.at(-1)?.order ?? "", ""), title, kind: "research", confidence: 0.5 + n / 20, sources: [{ url: `https://example.com/soil/${n}`, title: `Example source ${n}` }] } },
    }]);
    if (n === 3) {
      const first = kids[0];
      if (first) server.commitAs(mapId, researcher.id, [{ type: "node.update", payload: { id: first.id, append_body: "Researcher: confirmed with a second source." } }]);
    }
    server.setRun(runFor(researcher, ids.soil!, "claude", "running", { lens: "researcher", last_output: `Added "${title}"`, usage: { nodes: n, subagents: 0, depth: 1 } }));
  }, 5000);

  // Gardener: a standing proposal (re-proposed after it is resolved).
  const propose = () => {
    const live = (key: string) => !!server.state(mapId).doc.nodes.get(ids[key]!) && !server.state(mapId).doc.nodes.get(ids[key]!)!.deleted;
    const group = newId("nd");
    const drafts: OpDraft[] = [
      { type: "node.create", payload: { node: { id: group, parent_id: ids.plants!, order: "z", title: "Companion planting", kind: "idea" } } },
      { type: "node.create", payload: { node: { id: newId("nd"), parent_id: group, order: "V", title: "Marigolds with tomatoes", kind: "example" } } },
    ];
    if (live("plants2")) drafts.push({ type: "node.move", payload: { id: ids.plants2!, parent_id: ids.site!, order: "z" } });
    if (live("people3")) drafts.push({ type: "node.update", payload: { id: ids.people3!, set: { title: "Public liability insurance" } } });
    if (live("money2")) drafts.push({ type: "node.delete", payload: { id: ids.money2! } });
    drafts.push({ type: "link.create", payload: { link: { id: newId("ln"), from: ids.soil!, to: group, label: "feeds" } } });
    server.propose(mapId, gardener.run_id!, gardener.id, drafts, "Group companion plants, tidy the budget branch");
  };
  propose();
  setInterval(() => {
    const open = [...server.state(mapId).proposals.values()].some((p) => p.status === "open" || p.status === "ready");
    if (!open) propose();
  }, 20000);
}

if (BOTS) startBots();
httpServer.listen(PORT, "127.0.0.1", () => {
  console.log(`mindmap dev server: http://127.0.0.1:${PORT}/  (guest: http://127.0.0.1:${PORT}/m/${SHARE}/)`);
});
