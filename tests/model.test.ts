import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";

describe("manifest", () => {
  const manifest = readFileSync("manifest.yaml", "utf8");
  it("declares the mindmap_ping tool and route-matching id", () => {
    expect(manifest).toContain('id: "kandev-plugin-mindmap"');
    expect(manifest).toContain("name: mindmap_ping");
  });
  it("keeps the UI plugin id in sync with the manifest", () => {
    const ui = readFileSync("ui/src/register.tsx", "utf8");
    expect(ui).toContain('const PLUGIN_ID = "kandev-plugin-mindmap"');
  });
});
