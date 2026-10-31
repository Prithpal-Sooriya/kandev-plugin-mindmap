// Stdio MCP server exposing the 13 mind map tools as `mindmap_<name>`.
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { CallToolRequestSchema, ListToolsRequestSchema } from "@modelcontextprotocol/sdk/types.js";
import { callTool, errorResult, type CallOptions } from "./client.js";
import { byBridgeName, schemaFor, TOOLS, TOOL_PREFIX } from "./tools.js";
import { hasConfiguredToken } from "./token.js";

export const SERVER_NAME = "kandev-mindmap";
export const SERVER_VERSION = "0.1.0";

export function buildServer(opts: CallOptions = {}): Server {
  const env = opts.env ?? process.env;
  const server = new Server(
    { name: SERVER_NAME, version: SERVER_VERSION },
    {
      capabilities: { tools: {} },
      instructions:
        "Tools for the Kandev Mind Map plugin. Call mindmap_context first to see your map and run brief, " +
        "then read, search and add. Write tools return what was applied.",
    },
  );

  server.setRequestHandler(ListToolsRequestSchema, async () => {
    // Decided per request: a token configured later (config file) removes the run_token property.
    const withRunToken = !hasConfiguredToken(env);
    return {
      tools: TOOLS.map((t) => ({
        name: TOOL_PREFIX + t.name,
        description: t.description,
        inputSchema: schemaFor(t, withRunToken) as { type: "object"; properties?: Record<string, unknown> },
        annotations: {
          readOnlyHint: t.annotations?.read_only_hint,
          destructiveHint: t.annotations?.destructive_hint,
          idempotentHint: t.annotations?.idempotent_hint,
          openWorldHint: t.annotations?.open_world_hint,
        },
      })),
    };
  });

  server.setRequestHandler(CallToolRequestSchema, async (req, extra) => {
    const decl = byBridgeName(req.params.name);
    const result = decl
      ? await callTool(decl.name, (req.params.arguments ?? {}) as Record<string, unknown>, { ...opts, signal: extra.signal })
      : errorResult("bad_request", `unknown tool ${req.params.name}. Tools are named ${TOOL_PREFIX}<name>.`);
    return {
      content: [{ type: "text" as const, text: result.text }],
      ...(result.structured ? { structuredContent: result.structured } : {}),
      ...(result.is_error ? { isError: true } : {}),
    };
  });

  return server;
}
