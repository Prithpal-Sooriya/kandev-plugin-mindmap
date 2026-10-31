// Drives the MCP server with a real MCP client: in memory, and over stdio against the built single-file binary.
import { execFileSync } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { buildServer } from "../src/mcp-server.js";
import { TOOLS } from "../src/tools.js";
import { json, startFake } from "./fake-server.js";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const noConfig = { MINDMAP_CONFIG: "/nonexistent/kandev-mindmap/config.json" };
let fake: Awaited<ReturnType<typeof startFake>> | undefined;
afterEach(async () => {
  await fake?.close();
  fake = undefined;
});

async function connect(env: NodeJS.ProcessEnv) {
  const [a, b] = InMemoryTransport.createLinkedPair();
  await buildServer({ env }).connect(a);
  const client = new Client({ name: "test", version: "0" });
  await client.connect(b);
  return client;
}

describe("MCP server (in memory)", () => {
  it("lists 13 tools named mindmap_<name> with the contract schemas", async () => {
    const client = await connect({ ...noConfig, MINDMAP_TOKEN: `mmp1.1234.${"A".repeat(43)}` });
    const { tools } = await client.listTools();
    expect(tools.map((t) => t.name)).toEqual(TOOLS.map((t) => `mindmap_${t.name}`));
    for (const [i, t] of tools.entries()) {
      expect(t.inputSchema).toEqual(TOOLS[i].input_schema);
      expect(t.description).toBe(TOOLS[i].description);
    }
    expect(tools.find((t) => t.name === "mindmap_read")?.annotations).toMatchObject({ readOnlyHint: true, destructiveHint: false });
    expect(tools.find((t) => t.name === "mindmap_add")?.annotations?.readOnlyHint).toBe(false);
  });

  it("adds run_token only when no token is configured", async () => {
    const without = await (await connect(noConfig)).listTools();
    expect(without.tools.every((t) => "run_token" in (t.inputSchema.properties ?? {}))).toBe(true);
    const withTok = await (await connect({ ...noConfig, MINDMAP_TOKEN: `mmr1.1234.${"A".repeat(43)}` })).listTools();
    expect(withTok.tools.some((t) => "run_token" in (t.inputSchema.properties ?? {}))).toBe(false);
  });

  it("calls through to the plugin and maps results and errors", async () => {
    fake = await startFake((req, res) =>
      req.url.endsWith("/read")
        ? json(res, 200, { text: "- [nd_root] topic · idle · Root", structured: { rev: 7 } })
        : json(res, 401, { error: { code: "unauthorized", message: "no" } }),
    );
    const client = await connect({ ...noConfig, MINDMAP_TOKEN: fake.runToken });
    const ok = await client.callTool({ name: "mindmap_read", arguments: { depth: 1 } });
    expect(ok.isError).toBeFalsy();
    expect(ok.content).toEqual([{ type: "text", text: "- [nd_root] topic · idle · Root" }]);
    expect(ok.structuredContent).toEqual({ rev: 7 });
    expect(fake.calls[0].body).toEqual({ depth: 1 });

    const bad = await client.callTool({ name: "mindmap_search", arguments: { query: "x" } });
    expect(bad.isError).toBe(true);
    expect((bad.content as { text: string }[])[0].text).toMatch(/^error unauthorized/);

    const unknown = await client.callTool({ name: "mindmap_nope", arguments: {} });
    expect(unknown.isError).toBe(true);
  });
});

describe("MCP server (built binary over stdio)", () => {
  beforeAll(() => {
    execFileSync("node", ["scripts/build.mjs"], { cwd: root, stdio: "pipe" });
  });

  it("serves tools and relays a call", async () => {
    fake = await startFake((_r, res) => json(res, 200, { text: "- [nd_root] topic · idle · Root" }));
    const transport = new StdioClientTransport({
      command: "node",
      args: [join(root, "dist/kandev-mindmap-mcp.mjs")],
      env: { PATH: process.env.PATH ?? "", ...noConfig, MINDMAP_TOKEN: fake.runToken },
      stderr: "pipe",
    });
    const client = new Client({ name: "test", version: "0" });
    await client.connect(transport);
    try {
      expect((await client.listTools()).tools).toHaveLength(13);
      const out = await client.callTool({ name: "mindmap_context", arguments: {} });
      expect(out.isError).toBeFalsy();
      expect(fake.calls[0].url).toBe("/agent/v1/tools/context");
    } finally {
      await client.close();
    }
  });
});
