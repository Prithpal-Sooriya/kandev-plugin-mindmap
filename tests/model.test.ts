import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";

describe("manifest", () => {
  const manifest = readFileSync("manifest.yaml", "utf8");
  it("declares the contract tools and route-matching id", () => {
    expect(manifest).toContain('id: "kandev-plugin-mindmap"');
    expect(manifest).toContain("  - name: context\n");
    expect(manifest).not.toContain("mindmap_ping");
  });
  it("keeps the UI plugin id in sync with the manifest", () => {
    const ui = readFileSync("ui/src/kandev/register.tsx", "utf8");
    expect(ui).toContain('const PLUGIN_ID = "kandev-plugin-mindmap"');
  });
});
