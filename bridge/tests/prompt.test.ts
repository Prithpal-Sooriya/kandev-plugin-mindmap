import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { TOOLS, TOOL_PREFIX } from "../src/tools.js";

const root = join(dirname(fileURLToPath(import.meta.url)), "../..");
const prompt = readFileSync(join(root, "contract/prompts/bridge.md"), "utf8");

describe("bridge launch prompt", () => {
  it("names every bridge tool and the token placeholder", () => {
    for (const t of TOOLS) expect(prompt).toContain(`\`${TOOL_PREFIX}${t.name}\``);
    expect(prompt).toContain("{{RUN_TOKEN}}");
    expect(prompt).toContain("run_token");
  });

  it("describes the mindmap-patch fallback with a valid example", () => {
    const m = prompt.match(/```mindmap-patch\n([\s\S]*?)\n```/);
    expect(m).not.toBeNull();
    const patch = JSON.parse(m![1]) as { v: number; calls: { tool: string; args: unknown }[] };
    expect(patch.v).toBe(1);
    const names = TOOLS.map((t) => t.name);
    for (const c of patch.calls) expect(names).toContain(c.tool);
  });

  it("is mirrored in bridge/PROMPT.md (run `npm run sync:prompt --prefix bridge`)", () => {
    const doc = readFileSync(join(root, "bridge/PROMPT.md"), "utf8");
    expect(doc).toContain(`<!-- BEGIN bridge.md -->\n${prompt.trimEnd()}\n<!-- END bridge.md -->`);
  });
});
