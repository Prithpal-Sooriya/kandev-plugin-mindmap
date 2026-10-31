import { afterEach, describe, expect, it } from "vitest";
import mindmapExtension from "../src/pi.js";
import { TOOLS } from "../src/tools.js";
import { json, startFake } from "./fake-server.js";

let fake: Awaited<ReturnType<typeof startFake>> | undefined;
afterEach(async () => {
  await fake?.close();
  fake = undefined;
  delete process.env.MINDMAP_TOKEN;
  delete process.env.MINDMAP_CONFIG;
});

type Def = Parameters<Parameters<typeof mindmapExtension>[0]["registerTool"]>[0];
function load(): Def[] {
  const defs: Def[] = [];
  mindmapExtension({ registerTool: (d) => defs.push(d) });
  return defs;
}

describe("Pi extension", () => {
  it("registers the 13 tools with contract schemas", () => {
    process.env.MINDMAP_CONFIG = "/nonexistent/kandev-mindmap/config.json";
    process.env.MINDMAP_TOKEN = `mmp1.1234.${"A".repeat(43)}`;
    const defs = load();
    expect(defs.map((d) => d.name)).toEqual(TOOLS.map((t) => `mindmap_${t.name}`));
    for (const [i, d] of defs.entries()) {
      expect(d.parameters).toMatchObject(TOOLS[i].input_schema);
      expect(d.description).toBe(TOOLS[i].description);
    }
  });

  it("adds run_token when no token is configured", () => {
    process.env.MINDMAP_CONFIG = "/nonexistent/kandev-mindmap/config.json";
    for (const d of load()) expect((d.parameters as { properties: object }).properties).toHaveProperty("run_token");
  });

  it("executes via the plugin, returns text, and throws on tool errors", async () => {
    fake = await startFake((req, res) =>
      req.url.endsWith("/read")
        ? json(res, 200, { text: "- [nd_root] topic", structured: { rev: 2 } })
        : json(res, 200, { text: "error bad_request: nodes: required", is_error: true }),
    );
    process.env.MINDMAP_CONFIG = "/nonexistent/kandev-mindmap/config.json";
    process.env.MINDMAP_TOKEN = fake.runToken;
    const defs = load();
    const read = defs.find((d) => d.name === "mindmap_read")!;
    const ok = await read.execute("c1", { depth: 1 }, undefined, undefined, { cwd: "/tmp" });
    expect(ok).toEqual({ content: [{ type: "text", text: "- [nd_root] topic" }], details: { rev: 2 } });
    const add = defs.find((d) => d.name === "mindmap_add")!;
    await expect(add.execute("c2", {}, undefined, undefined, {})).rejects.toThrow(/error bad_request/);
  });
});
