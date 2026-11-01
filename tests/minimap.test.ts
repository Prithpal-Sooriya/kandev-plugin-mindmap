import { readFileSync } from "node:fs";
import ReactLib from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeAll, expect, test } from "vitest";
import type { MapController } from "../ui/src/canvas/controller";
import { Minimap } from "../ui/src/canvas/minimap";
import { bindReact, h } from "../ui/src/runtime";

beforeAll(() => bindReact(ReactLib));

function renderMinimap() {
  const ctl = {
    display: { layout: { bounds: { x: 0, y: 0, w: 1000, h: 400 }, boxes: new Map() } },
    engine: { runs: new Map(), participants: new Map() },
  } as unknown as MapController;
  return renderToStaticMarkup(h(Minimap, {
    ctl,
    region: { x: 10, y: 20, w: 100, h: 80 },
    onNavigate: () => {},
  }));
}

test("minimap is a small overlay and the camera can move its viewport rect", () => {
  const html = renderMinimap();
  expect(html).toContain('class="mm-minimap"');
  expect(html).toContain('id="mm-minimap-vp"');
  expect(html).not.toContain("--bg-muted");
  expect(html).toContain('fill="var(--muted)"');

  const css = readFileSync("ui/plugin.css", "utf8");
  const block = css.match(/\.mm-minimap\s*\{[^}]+\}/)?.[0] ?? "";
  expect(block).toMatch(/position:\s*absolute/);
  expect(block).toMatch(/width:\s*180px/);
  expect(block).toMatch(/height:\s*110px/);
  expect(block).toMatch(/overflow:\s*hidden/);
});
