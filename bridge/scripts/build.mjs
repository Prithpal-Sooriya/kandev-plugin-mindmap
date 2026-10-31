#!/usr/bin/env node
// Builds the two single-file bridge outputs into bridge/dist/.
import { build } from "esbuild";
import { chmodSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const dist = join(root, "dist");
mkdirSync(dist, { recursive: true });

const common = { bundle: true, platform: "node", format: "esm", target: "node20", legalComments: "none", logLevel: "warning" };

// stdio MCP server: everything bundled, including @modelcontextprotocol/sdk.
const mcpOut = join(dist, "kandev-mindmap-mcp.mjs");
await build({
  ...common,
  entryPoints: [join(root, "src/mcp.ts")],
  outfile: mcpOut,
  banner: {
    js: "#!/usr/bin/env node\nimport { createRequire as __cr } from 'node:module'; const require = __cr(import.meta.url);",
  },
});
chmodSync(mcpOut, 0o755);

// Pi extension: typebox and Pi's own packages are provided by the host.
await build({
  ...common,
  entryPoints: [join(root, "src/pi.ts")],
  outfile: join(dist, "pi-extension.mjs"),
  external: ["typebox", "@earendil-works/*"],
});

// Sanity: the shebang must be the first line, once.
const head = readFileSync(mcpOut, "utf8");
if (!head.startsWith("#!/usr/bin/env node\n") || head.indexOf("#!", 2) !== -1) {
  writeFileSync(mcpOut, head.replace(/^(#!.*\n)+/, "#!/usr/bin/env node\n"));
}
console.log("bridge built: dist/kandev-mindmap-mcp.mjs, dist/pi-extension.mjs");
