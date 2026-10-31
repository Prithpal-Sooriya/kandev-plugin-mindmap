// TS side of the contract parity check. server/contract/*_test.go runs the
// same fixtures through the Go types; if both pass, the shapes agree.
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import {
  CLIENT_MSG_TYPES, ERROR_CODES, FIXTURE_SHAPES, HTTP_STATUS, LIMITS, OP_TYPES, SERVER_MSG_TYPES, TOOL_NAMES,
  buildParentIndex, checkMerge, checkMove, depth, newId, orderBetween, parseToken, validId, validNodeRef,
  validOrderKey, validate, type Ctx, type Op, type ServerMessage,
} from "../ui/src/contract";
import { unknownKeywords, validateSchema, type JsonSchema } from "./jsonschema-lite";

const FIXTURES = "contract/fixtures";
const TOOLS = "contract/tools";
const NON_SHAPE = new Set(["OrderCases"]);

const readJson = (path: string): unknown => JSON.parse(readFileSync(path, "utf8"));
const fixture = (name: string): unknown[] => readJson(join(FIXTURES, `${name}.json`)) as unknown[];

describe("fixtures", () => {
  it("match the shape registry (same list as Go fixtureTypes)", () => {
    const files = readdirSync(FIXTURES)
      .filter((f) => f.endsWith(".json"))
      .map((f) => f.slice(0, -5))
      .filter((n) => !NON_SHAPE.has(n));
    expect(files.sort()).toEqual(Object.keys(FIXTURE_SHAPES).sort());
  });

  it("validate strictly and exercise every field of every shape", () => {
    const ctx: Ctx = { errors: [], cov: new Map() };
    for (const [name, shape] of Object.entries(FIXTURE_SHAPES)) {
      const items = fixture(name);
      expect(items.length, name).toBeGreaterThan(0);
      items.forEach((item, i) => shape.check(item, `${name}[${i}]`, ctx));
    }
    expect(ctx.errors).toEqual([]);
    const missing: string[] = [];
    for (const [shape, keys] of ctx.cov!) for (const [k, seen] of keys) if (!seen) missing.push(`${shape}.${k}`);
    expect(missing).toEqual([]);
  });

  it("cover every op type and message type", () => {
    const ops = new Set((fixture("Op") as Op[]).map((o) => o.type));
    expect([...OP_TYPES].filter((t) => !ops.has(t))).toEqual([]);
    const msgs = new Set([...fixture("ClientMessage"), ...fixture("ServerMessage")].map((m) => (m as { type: string }).type));
    expect([...CLIENT_MSG_TYPES, ...SERVER_MSG_TYPES].filter((t) => !msgs.has(t))).toEqual([]);
  });

  it("reject unknown fields, nulls and wrong tags", () => {
    const op = structuredClone(fixture("Op")[0]) as Record<string, unknown>;
    expect(validate(FIXTURE_SHAPES.Op!, { ...op, extra: 1 })).not.toEqual([]);
    expect(validate(FIXTURE_SHAPES.Op!, { ...op, proposal_id: null })).not.toEqual([]);
    expect(validate(FIXTURE_SHAPES.Op!, { ...op, type: "node.explode" })).not.toEqual([]);
    expect(validate(FIXTURE_SHAPES.Op!, { ...op, type: "node.delete" })).not.toEqual([]); // payload mismatch
  });

  it("narrow tagged unions statically", () => {
    const msg = fixture("ServerMessage")[3] as ServerMessage;
    if (msg.type === "ops") {
      const first = msg.payload.ops[0]!;
      if (first.type === "node.move") expect(typeof first.payload.parent_id).toBe("string");
    }
    expect(msg.type).toBe("ops");
  });
});

describe("order keys", () => {
  it("match the Go implementation on shared cases", () => {
    for (const [a, b, want] of readJson(join(FIXTURES, "OrderCases.json")) as [string, string, string][]) {
      expect(orderBetween(a, b), `${a}|${b}`).toBe(want);
    }
  });
  it("stay ordered under repeated inserts", () => {
    let lo = "", hi = "";
    for (let i = 0; i < 200; i++) {
      const k = orderBetween(lo, hi);
      expect(validOrderKey(k)).toBe(true);
      if (lo) expect(k > lo).toBe(true);
      if (hi) expect(k < hi).toBe(true);
      if (i % 2 === 0) lo = k;
      else hi = k;
    }
  });
});

describe("tree checks", () => {
  const nodes = [
    { id: "nd_root000000" }, { id: "nd_a000000000", parent_id: "nd_root000000" },
    { id: "nd_b000000000", parent_id: "nd_a000000000" }, { id: "nd_d000000000", parent_id: "nd_root000000" },
  ];
  const idx = buildParentIndex(nodes);
  it("mirror Go CheckMove/CheckMerge", () => {
    expect(checkMove(idx, "nd_root000000", "nd_b000000000", "nd_d000000000")).toBeNull();
    expect(checkMove(idx, "nd_root000000", "nd_a000000000", "nd_b000000000")).toBe("cycle");
    expect(checkMove(idx, "nd_root000000", "nd_root000000", "nd_a000000000")).toBe("root_immutable");
    expect(checkMove(idx, "nd_root000000", "nd_x000000000", "nd_a000000000")).toBe("not_found");
    expect(checkMerge(idx, "nd_root000000", "nd_b000000000", ["nd_a000000000"])).toBe("cycle");
    expect(depth(idx, "nd_b000000000")).toBe(2);
  });
});

describe("ids, tokens and errors", () => {
  it("mint and parse", () => {
    const id = newId("nd");
    expect(validId("nd", id)).toBe(true);
    expect(validNodeRef(id) && validNodeRef("@tiers") && !validNodeRef("tiers")).toBe(true);
    const secret = "A".repeat(42) + "_";
    expect(parseToken(`mmr1.52593.${secret}`)).toEqual({ kind: "mmr", port: 52593, secret });
    expect(parseToken(`mms1.${secret}`)).toEqual({ kind: "mms", secret });
    expect(parseToken(`mmr1.${secret}`)).toBeNull();
  });
  it("map every code to an HTTP status", () => {
    for (const c of ERROR_CODES) expect(HTTP_STATUS[c], c).toBeGreaterThanOrEqual(400);
  });
});

describe("agent tools", () => {
  const decls = TOOL_NAMES.map((n) => readJson(join(TOOLS, `${n}.json`)) as {
    name: string; description: string; annotations: Record<string, boolean>; input_schema: JsonSchema;
  });

  it("has one file per tool, in manifest order", () => {
    expect(readdirSync(TOOLS).sort()).toEqual(TOOL_NAMES.map((n) => `${n}.json`).sort());
    expect(decls.map((d) => d.name)).toEqual([...TOOL_NAMES]);
  });

  it("uses only portable keywords and fits Kandev limits", () => {
    let total = 0;
    for (const d of decls) {
      expect(unknownKeywords(d.input_schema), d.name).toEqual([]);
      expect(new TextEncoder().encode(d.description).length, d.name).toBeLessThanOrEqual(1024);
      total += JSON.stringify(d.input_schema).length;
    }
    expect(decls.length).toBeLessThanOrEqual(16);
    expect(total).toBeLessThan(64 * 1024);
  });

  it("agrees with LIMITS", () => {
    const add = decls.find((d) => d.name === "add")!.input_schema as any;
    const node = add.properties.nodes.items;
    expect(node.properties.title.maxLength).toBe(LIMITS.title);
    expect(node.properties.body.maxLength).toBe(LIMITS.body);
    expect(node.properties.tags.maxItems).toBe(LIMITS.tags);
    expect(node.properties.sources.items.properties.quote.maxLength).toBe(LIMITS.quote);
    let levels = 0;
    for (let n = node; n; n = n.properties.children?.items) levels++;
    expect(levels).toBe(LIMITS.addDepth);
  });

  for (const name of TOOL_NAMES) {
    it(`argument fixtures for ${name}`, () => {
      const schema = decls.find((d) => d.name === name)!.input_schema;
      const cases = readJson(join(FIXTURES, "tool-args", `${name}.json`)) as Record<"valid" | "invalid" | "semantic", unknown[]>;
      for (const v of cases.valid) expect(validateSchema(schema, v), JSON.stringify(v)).toEqual([]);
      for (const v of cases.invalid) expect(validateSchema(schema, v), JSON.stringify(v)).not.toEqual([]);
      // Semantic cases pass the schema; Go's DecodeToolArgs rejects them (Go test).
      for (const v of cases.semantic) expect(validateSchema(schema, v), JSON.stringify(v)).toEqual([]);
    });
  }

  it("manifest.yaml agent_tools is in sync with contract/tools", () => {
    expect(() => execFileSync(process.execPath, ["scripts/sync-manifest-tools.mjs", "--check"], { stdio: "pipe" })).not.toThrow();
  });
});
