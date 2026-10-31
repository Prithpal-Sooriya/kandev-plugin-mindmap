#!/usr/bin/env node
// Builds the dev UI (guest app + fake-server dev server) and starts it.
// Used by `npm run dev` and the .claude/launch.json preview entry.
import { execFileSync } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
process.chdir(root);
execFileSync(process.execPath, ["scripts/build-ui.mjs", "--dev"], { stdio: "inherit" });
await import(pathToFileURL(join(root, ".build/devserver.mjs")).href);
