import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { callTool } from "../src/client.js";
import { json, startFake } from "./fake-server.js";

const noConfig = { MINDMAP_CONFIG: "/nonexistent/kandev-mindmap/config.json" };
let fake: Awaited<ReturnType<typeof startFake>> | undefined;
afterEach(async () => {
  await fake?.close();
  fake = undefined;
});

describe("callTool", () => {
  it("posts the arguments untouched with the bearer token and contract headers", async () => {
    fake = await startFake((_r, res) => json(res, 200, { text: "- [nd_x] idea · idle · Root", structured: { rev: 3 } }));
    const args = { node_id: "nd_x", depth: 2, include_bodies: false };
    const out = await callTool("read", args, { env: { ...noConfig, MINDMAP_TOKEN: fake.runToken } });
    expect(out).toEqual({ text: "- [nd_x] idea · idle · Root", structured: { rev: 3 } });
    const [call] = fake.calls;
    expect(call.method).toBe("POST");
    expect(call.url).toBe("/agent/v1/tools/read");
    expect(call.headers.authorization).toBe(`Bearer ${fake.runToken}`);
    expect(call.headers["x-mindmap-contract"]).toBe("1");
    expect(call.body).toEqual(args);
  });

  it("uses run_token only when nothing is configured, and strips it", async () => {
    fake = await startFake((_r, res) => json(res, 200, { text: "ok" }));
    const out = await callTool("search", { query: "pricing", run_token: fake.runToken }, { env: noConfig });
    expect(out.text).toBe("ok");
    expect(fake.calls[0].body).toEqual({ query: "pricing" });
    expect(fake.calls[0].headers.authorization).toBe(`Bearer ${fake.runToken}`);

    // A configured token wins over run_token, which is still stripped.
    await callTool("search", { query: "x", run_token: "mmr1.1.ignored" }, { env: { ...noConfig, MINDMAP_TOKEN: fake.personalToken } });
    expect(fake.calls[1].body).toEqual({ query: "x" });
    expect(fake.calls[1].headers.authorization).toBe(`Bearer ${fake.personalToken}`);
  });

  it("passes tool errors (HTTP 200, is_error) through unchanged", async () => {
    const result = {
      text: "error bad_request: nodes: required: at least one node",
      structured: { error: { code: "bad_request", field: "nodes" } },
      is_error: true,
    };
    fake = await startFake((_r, res) => json(res, 200, result));
    expect(await callTool("add", {}, { env: { ...noConfig, MINDMAP_TOKEN: fake.runToken } })).toEqual(result);
  });

  it("explains a missing token without calling the plugin", async () => {
    const out = await callTool("read", {}, { env: noConfig });
    expect(out.is_error).toBe(true);
    expect(out.text).toMatch(/^error unauthorized: no Mind Map token/);
  });

  describe("error mapping", () => {
    const cases: [number, unknown, RegExp][] = [
      [401, "", /^error unauthorized: .*rejected the token/],
      [404, { error: { code: "not_found", message: "no such tool" } }, /^error not_found: no such tool/],
      [413, "", /^error too_large: .*16 MiB/],
      [426, "", /^error version: .*contract/],
      [429, { error: { code: "rate_limited", message: "slow down", retry_ms: 750 } }, /^error rate_limited: slow down; retry in 750 ms/],
      [500, { error: { code: "internal", message: "boom" } }, /^error internal: boom.*plugin-side/],
      [503, "", /^error unavailable: .*HTTP 503/],
      [400, { error: { code: "bad_request", message: "bad json", field: "args" } }, /^error bad_request: bad json/],
    ];
    it.each(cases)("maps HTTP %i", async (status, body, pattern) => {
      fake = await startFake((_r, res) => {
        res.writeHead(status);
        res.end(typeof body === "string" ? body : JSON.stringify(body));
      });
      const out = await callTool("read", {}, { env: { ...noConfig, MINDMAP_TOKEN: fake.runToken } });
      expect(out.is_error).toBe(true);
      expect(out.text).toMatch(pattern);
      expect((out.structured?.error as { code: string }).code).toBeTruthy();
    });

    it("flags a malformed 200 body", async () => {
      fake = await startFake((_r, res) => {
        res.writeHead(200);
        res.end("<html>");
      });
      const out = await callTool("read", {}, { env: { ...noConfig, MINDMAP_TOKEN: fake.runToken } });
      expect(out.text).toMatch(/^error internal: .*malformed/);
    });

    it("reports an unreachable plugin with the URL", async () => {
      fake = await startFake((_r, res) => json(res, 200, { text: "ok" }));
      const token = fake.runToken;
      await fake.close();
      fake = undefined;
      const out = await callTool("read", {}, { env: { ...noConfig, MINDMAP_TOKEN: token } });
      expect(out.text).toMatch(/^error unavailable: cannot reach the Mind Map plugin at http:\/\/127\.0\.0\.1:\d+/);
    });

    it("times out with a retry-safety hint", async () => {
      fake = await startFake(() => new Promise(() => {}));
      const out = await callTool("add", {}, { env: { ...noConfig, MINDMAP_TOKEN: fake.runToken }, timeoutMs: 150 });
      expect(out.text).toMatch(/^error unavailable: .*did not answer.*read the map before retrying/);
      fake.calls.length = 0;
    });
  });

  describe("attach", () => {
    it("reads a relative file_path itself and sends base64", async () => {
      fake = await startFake((_r, res) => json(res, 200, { text: "attached" }));
      const dir = mkdtempSync(join(tmpdir(), "mm-attach-"));
      writeFileSync(join(dir, "shot.png"), Buffer.from([1, 2, 3, 4]));
      const out = await callTool(
        "attach",
        { node_id: "nd_x", file_path: "shot.png", caption: "synthetic" },
        { env: { ...noConfig, MINDMAP_TOKEN: fake.runToken }, cwd: dir },
      );
      expect(out.text).toBe("attached");
      expect(fake.calls[0].body).toEqual({
        node_id: "nd_x",
        caption: "synthetic",
        base64: Buffer.from([1, 2, 3, 4]).toString("base64"),
        mime_type: "image/png",
        filename: "shot.png",
      });
    });

    it("leaves absolute paths and URLs to the plugin", async () => {
      fake = await startFake((_r, res) => json(res, 200, { text: "attached" }));
      const env = { ...noConfig, MINDMAP_TOKEN: fake.runToken };
      await callTool("attach", { node_id: "n", file_path: "/abs/shot.png" }, { env });
      await callTool("attach", { node_id: "n", url: "https://example.com/a.png" }, { env });
      expect(fake.calls[0].body).toEqual({ node_id: "n", file_path: "/abs/shot.png" });
      expect(fake.calls[1].body).toEqual({ node_id: "n", url: "https://example.com/a.png" });
    });

    it("errors clearly for a missing file or unknown type, without calling the plugin", async () => {
      fake = await startFake((_r, res) => json(res, 200, { text: "attached" }));
      const dir = mkdtempSync(join(tmpdir(), "mm-attach-"));
      writeFileSync(join(dir, "blob.xyz"), "x");
      const env = { ...noConfig, MINDMAP_TOKEN: fake.runToken };
      expect((await callTool("attach", { file_path: "nope.png" }, { env, cwd: dir })).text).toMatch(/^error fetch_failed: file_path: cannot read/);
      expect((await callTool("attach", { file_path: "blob.xyz" }, { env, cwd: dir })).text).toMatch(/^error unsupported_media: .*mime_type/);
      expect(fake.calls).toHaveLength(0);
    });
  });
});
