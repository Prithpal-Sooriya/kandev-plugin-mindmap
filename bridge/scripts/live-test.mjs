#!/usr/bin/env node
// Live check against an installed plugin, through the built stdio MCP server and a real MCP client.
//
//   KANDEV_URL=http://127.0.0.1:38430 node scripts/live-test.mjs
//
// Steps: `session` webhook -> local token -> POST /m/<t>/api/tokens/personal -> personal token ->
// spawn dist/kandev-mindmap-mcp.mjs with MINDMAP_TOKEN -> list tools, call `read` and `search`.
// If no map exists it creates one titled "Bridge live test (synthetic)". Tokens are never printed.
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const kandev = (process.env.KANDEV_URL || "http://127.0.0.1:38430").replace(/\/+$/, "");
const redact = (s) => String(s).replace(/mm[lrpsg]1\.[A-Za-z0-9._-]+/g, "mm?1.<redacted>");

async function jfetch(url, init) {
  const res = await fetch(url, init);
  const text = await res.text();
  if (!res.ok) throw new Error(`${init?.method ?? "GET"} ${redact(url)} -> ${res.status}: ${redact(text).slice(0, 300)}`);
  return JSON.parse(text);
}
const post = (url, body, headers = {}) =>
  jfetch(url, { method: "POST", headers: { "Content-Type": "application/json", ...headers }, body: JSON.stringify(body ?? {}) });

const session = await post(`${kandev}/api/plugins/kandev-plugin-mindmap/webhooks/session`, {});
const base = session.base_url;
const local = session.token;
console.log(`session ok: loopback ${base}, contract ${session.contract}`);

const personal = await post(`${base}/m/${local}/api/tokens/personal`, { label: "bridge live test" });
console.log(`personal token issued (label "${personal.label}")`);

let maps = await jfetch(`${base}/m/${local}/api/maps`);
maps = Array.isArray(maps) ? maps : (maps.maps ?? []);
let map = maps[0];
if (!map) {
  map = await post(`${base}/m/${local}/api/maps`, {
    workspace_id: process.env.KANDEV_WORKSPACE_ID || "default",
    title: "Bridge live test (synthetic)",
  });
  console.log("created synthetic map");
}
console.log(`using map ${map.id}, title: ${map.title}`);

const client = new Client({ name: "bridge-live-test", version: "0" });
await client.connect(
  new StdioClientTransport({
    command: "node",
    args: [join(root, "dist/kandev-mindmap-mcp.mjs")],
    env: { PATH: process.env.PATH ?? "", MINDMAP_CONFIG: "/nonexistent/config.json", MINDMAP_TOKEN: personal.token },
    stderr: "pipe",
  }),
);

let failed = false;
function show(label, out, expectError = false) {
  const text = out.content?.map((c) => c.text).join("\n") ?? "";
  console.log(`\n--- ${label} (isError=${Boolean(out.isError)}) ---\n${redact(text).slice(0, 800)}`);
  if (out.structuredContent) console.log(`structured: ${redact(JSON.stringify(out.structuredContent)).slice(0, 300)}`);
  if (Boolean(out.isError) !== expectError) failed = true;
}

try {
  const { tools } = await client.listTools();
  console.log(`\ntools/list: ${tools.length} tools: ${tools.map((t) => t.name).join(", ")}`);
  if (tools.length !== 13) failed = true;

  // A personal token has no run: read needs map_id, and search needs a node_id to pick the map.
  const readOut = await client.callTool({ name: "mindmap_read", arguments: { map_id: map.id, depth: 2 } });
  show("mindmap_read (map_id, depth 2)", readOut);
  const nodeId = readOut.content?.[0]?.text?.match(/\[(nd_[A-Za-z0-9]+)\]/)?.[1];
  if (!nodeId) throw new Error("no node id in read output");
  const word = String(map.title).split(/\s+/)[0] || "root";
  show(`mindmap_search "${word}" (scope map, node_id ${nodeId})`, await client.callTool({ name: "mindmap_search", arguments: { node_id: nodeId, query: word } }));
  // A bad call must come back as a readable tool error, not a crash.
  show("mindmap_read (bad depth, expected error)", await client.callTool({ name: "mindmap_read", arguments: { map_id: map.id, depth: 99 } }), true);
} finally {
  await client.close();
}
process.exit(failed ? 1 : 0);
