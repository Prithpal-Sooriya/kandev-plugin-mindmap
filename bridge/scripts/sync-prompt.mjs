#!/usr/bin/env node
// Copies contract/prompts/bridge.md into the marked block of bridge/PROMPT.md.
//   node scripts/sync-prompt.mjs          rewrite
//   node scripts/sync-prompt.mjs --check  exit 1 if stale
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const BEGIN = "<!-- BEGIN bridge.md -->";
const END = "<!-- END bridge.md -->";
const source = readFileSync(join(root, "../contract/prompts/bridge.md"), "utf8").trimEnd();
const file = join(root, "PROMPT.md");
const doc = readFileSync(file, "utf8");
const a = doc.indexOf(BEGIN);
const b = doc.indexOf(END);
if (a < 0 || b < a) throw new Error("PROMPT.md is missing the BEGIN/END markers");
const next = `${doc.slice(0, a + BEGIN.length)}\n${source}\n${doc.slice(b)}`;
if (process.argv.includes("--check")) {
  if (next !== doc) {
    console.error("bridge/PROMPT.md is stale: run `npm run sync:prompt --prefix bridge`");
    process.exit(1);
  }
} else if (next !== doc) {
  writeFileSync(file, next);
  console.log("bridge/PROMPT.md updated");
}
