import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { byBridgeName, schemaFor, TOOLS } from "../src/tools.js";

const toolsDir = join(dirname(fileURLToPath(import.meta.url)), "../../contract/tools");
const onDisk = readdirSync(toolsDir).filter((f) => f.endsWith(".json")).map((f) => f.replace(/\.json$/, ""));

describe("embedded tool declarations", () => {
  it("covers every contract/tools/*.json file", () => {
    expect(TOOLS.map((t) => t.name).sort()).toEqual(onDisk.sort());
    expect(TOOLS).toHaveLength(13);
  });

  it("passes each schema through unchanged when a token is configured", () => {
    for (const t of TOOLS) {
      const file = JSON.parse(readFileSync(join(toolsDir, `${t.name}.json`), "utf8"));
      expect(schemaFor(t, false)).toEqual(file.input_schema);
    }
  });

  it("adds only a run_token property when no token is configured, without mutating the source", () => {
    for (const t of TOOLS) {
      const base = schemaFor(t, false) as { properties: Record<string, unknown>; additionalProperties: boolean };
      const withTok = schemaFor(t, true) as typeof base;
      expect(Object.keys(withTok.properties).sort()).toEqual([...Object.keys(base.properties), "run_token"].sort());
      expect(withTok.additionalProperties).toBe(false);
      expect(schemaFor(t, false)).toEqual(base);
      expect(t.input_schema.properties).not.toHaveProperty("run_token");
    }
  });

  it("resolves bridge names", () => {
    expect(byBridgeName("mindmap_read")?.name).toBe("read");
    expect(byBridgeName("read")).toBeUndefined();
    expect(byBridgeName("mindmap_nope")).toBeUndefined();
  });
});
