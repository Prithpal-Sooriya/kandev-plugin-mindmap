import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { baseUrlFor, hasConfiguredToken, parseBridgeToken, resolveToken, TokenError } from "../src/token.js";

const S = "B".repeat(43);
const run = (port: number) => `mmr1.${port}.${S}`;
const personal = (port: number) => `mmp1.${port}.${S}`;
const noConfig = { MINDMAP_CONFIG: "/nonexistent/kandev-mindmap/config.json" };

function configFile(content: string): string {
  const dir = mkdtempSync(join(tmpdir(), "mm-bridge-"));
  const p = join(dir, "config.json");
  writeFileSync(p, content);
  return p;
}

describe("parseBridgeToken", () => {
  it("accepts run and personal tokens and reads the port", () => {
    expect(parseBridgeToken(run(41234))).toEqual({ ok: true, kind: "run", port: 41234 });
    expect(parseBridgeToken(personal(80))).toEqual({ ok: true, kind: "personal", port: 80 });
  });
  it("rejects canvas tokens with a pointer to the right kind", () => {
    const r = parseBridgeToken(`mml1.${S}`);
    expect(r.ok).toBe(false);
    expect(!r.ok && r.reason).toContain("canvas UI");
  });
  it.each([
    ["garbage", "expected a run token"],
    [`mmr1.${S}`, "look like"],
    [`mmr1.0.${S}`, "port"],
    [`mmr1.70000.${S}`, "port"],
    [`mmr1.1234.short`, "secret"],
  ])("rejects %s", (raw, why) => {
    const r = parseBridgeToken(raw);
    expect(r.ok).toBe(false);
    expect(!r.ok && r.reason).toContain(why);
  });
});

describe("resolveToken order", () => {
  it("prefers MINDMAP_TOKEN over the config file over run_token", () => {
    const cfg = configFile(JSON.stringify({ token: personal(2222) }));
    const env = { MINDMAP_TOKEN: run(1111), MINDMAP_CONFIG: cfg };
    expect(resolveToken(run(3333), env)).toMatchObject({ source: "env", port: 1111, kind: "run" });
    expect(resolveToken(run(3333), { MINDMAP_CONFIG: cfg })).toMatchObject({ source: "config", port: 2222, kind: "personal" });
    expect(resolveToken(run(3333), noConfig)).toMatchObject({ source: "arg", port: 3333 });
  });

  it("explains how to configure a token when none is found", () => {
    expect(() => resolveToken(undefined, noConfig)).toThrow(/MINDMAP_TOKEN.*run_token/s);
    expect(() => resolveToken("   ", noConfig)).toThrow(TokenError);
  });

  it("names the source of an invalid token", () => {
    expect(() => resolveToken(undefined, { MINDMAP_TOKEN: "nope", ...noConfig })).toThrow(/invalid token from env/);
  });

  it("reports a broken config file", () => {
    const env = { MINDMAP_CONFIG: configFile("{not json") };
    expect(() => resolveToken(run(1), env)).toThrow(/not valid JSON/);
    expect(hasConfiguredToken(env)).toBe(false);
  });

  it("ignores a config file without a token", () => {
    expect(hasConfiguredToken({ MINDMAP_CONFIG: configFile("{}") })).toBe(false);
  });
});

describe("baseUrlFor", () => {
  it("derives the loopback URL from the token port and honours MINDMAP_URL", () => {
    expect(baseUrlFor(4242, {})).toBe("http://127.0.0.1:4242");
    expect(baseUrlFor(4242, { MINDMAP_URL: "http://localhost:9/" })).toBe("http://localhost:9");
  });
});
