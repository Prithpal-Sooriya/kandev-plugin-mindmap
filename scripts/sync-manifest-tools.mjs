#!/usr/bin/env node
// Regenerates the agent_tools block of manifest.yaml from contract/tools/*.json.
// contract/tools is the source of truth; never edit the generated block by hand.
//
//   node scripts/sync-manifest-tools.mjs          rewrite manifest.yaml
//   node scripts/sync-manifest-tools.mjs --check  exit 1 if manifest.yaml is stale
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
export const BEGIN = "# BEGIN generated agent_tools: edit contract/tools/*.json, then run `npm run sync:tools`";
export const END = "# END generated agent_tools";

// Manifest order; matches contract.ToolNames in Go and TOOL_NAMES in TS.
export const TOOL_ORDER = [
  "context", "read", "search", "add", "update", "attach", "restructure",
  "link", "claim", "spawn", "ask_human", "write_doc", "report",
];

export function loadTools(dir = join(root, "contract/tools")) {
  return TOOL_ORDER.map((name) => JSON.parse(readFileSync(join(dir, `${name}.json`), "utf8")));
}

// JSON is valid YAML flow syntax; continuation lines are indented past the key.
function flow(value, indent) {
  return JSON.stringify(value, null, 2).replace(/\n/g, "\n" + " ".repeat(indent));
}

export function renderBlock(tools) {
  const lines = [BEGIN, "agent_tools:"];
  for (const t of tools) {
    lines.push(`  - name: ${t.name}`);
    lines.push(`    description: ${JSON.stringify(t.description)}`);
    lines.push(`    surfaces: [kanban-task, office-task]`);
    lines.push(`    annotations: ${JSON.stringify(t.annotations)}`);
    lines.push(`    input_schema: ${flow(t.input_schema, 6)}`);
  }
  lines.push(END);
  return lines.join("\n");
}

export function syncManifest(manifest, tools) {
  const start = manifest.indexOf(BEGIN);
  const end = manifest.indexOf(END);
  if (start < 0 || end < start) throw new Error("manifest.yaml is missing the generated agent_tools markers");
  return manifest.slice(0, start) + renderBlock(tools) + manifest.slice(end + END.length);
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const path = join(root, "manifest.yaml");
  const current = readFileSync(path, "utf8");
  const next = syncManifest(current, loadTools());
  if (process.argv.includes("--check")) {
    if (next !== current) {
      console.error("manifest.yaml agent_tools is stale: run `npm run sync:tools`");
      process.exit(1);
    }
  } else if (next !== current) {
    writeFileSync(path, next);
    console.log("manifest.yaml agent_tools updated");
  }
}
