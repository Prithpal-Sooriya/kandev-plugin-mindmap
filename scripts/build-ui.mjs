#!/usr/bin/env node
// Builds both UI entries:
//   ui/bundle.js        in-Kandev plugin bundle (host React via the registry API; no React inside)
//   server/web/dist/    standalone guest app (own React 19 + react-dom), embedded by the Go backend
// --dev also builds a dev copy of the guest app (seed + perf overlay) and the fake-server dev server.
import { build } from "esbuild";
import { mkdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { gzipSync } from "node:zlib";

const dev = process.argv.includes("--dev");
const common = {
  bundle: true,
  format: "esm",
  target: "es2022",
  jsx: "transform",
  jsxFactory: "h",
  jsxFragment: "Fragment",
  legalComments: "none",
  logLevel: "warning",
};

const INDEX = (title) => `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>${title}</title>
<link rel="stylesheet" href="./app.css">
</head>
<body>
<div id="root"><p style="font:14px system-ui,sans-serif;padding:24px;opacity:.7">Loading Mind Map… If this stays, the app bundle is missing: run <code>make bundle</code>.</p></div>
<script type="module" src="./app.js"></script>
</body>
</html>
`;

async function standalone(outdir, isDev) {
  mkdirSync(outdir, { recursive: true });
  await build({
    ...common,
    entryPoints: { app: "ui/src/standalone/main.tsx" },
    outdir,
    minify: !isDev,
    sourcemap: isDev ? "inline" : false,
    define: { __DEV__: String(isDev), "process.env.NODE_ENV": JSON.stringify(isDev ? "development" : "production") },
  });
  writeFileSync(`${outdir}/index.html`, INDEX("Mind Map"));
}

await build({
  ...common,
  entryPoints: ["ui/src/kandev/register.tsx"],
  outfile: "ui/bundle.js",
  minify: true,
  define: { __DEV__: "false" },
});
const kandev = readFileSync("ui/bundle.js", "utf8");
if (/react-dom|__SECRET_INTERNALS|react\.production/.test(kandev)) {
  console.error("ui/bundle.js must not bundle React: use the host React (api.React / api.jsx).");
  process.exit(1);
}

await standalone("server/web/dist", false);

if (dev) {
  await standalone(".build/dev-web", true);
  await build({
    ...common,
    entryPoints: ["ui/dev/kandev-host.tsx"],
    outfile: ".build/kandev-host.js",
    define: { "process.env.NODE_ENV": '"development"' },
  });
  await build({
    ...common,
    entryPoints: ["ui/dev/devserver.ts"],
    outfile: ".build/devserver.mjs",
    platform: "node",
    define: { __DEV__: "true" },
  });
}

const size = (f) => {
  const b = readFileSync(f);
  return `${(statSync(f).size / 1024).toFixed(1)} KiB (${(gzipSync(b).length / 1024).toFixed(1)} KiB gzip)`;
};
console.log(`ui/bundle.js              ${size("ui/bundle.js")}`);
console.log(`ui/plugin.css             ${size("ui/plugin.css")}`);
console.log(`server/web/dist/app.js    ${size("server/web/dist/app.js")}`);
console.log(`server/web/dist/app.css   ${size("server/web/dist/app.css")}`);
