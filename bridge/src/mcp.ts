// kandev-mindmap-mcp: stdio MCP server entry point.
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { buildServer } from "./mcp-server.js";

await buildServer().connect(new StdioServerTransport());
// stdout carries the protocol; log only to stderr.
console.error("kandev-mindmap-mcp ready (13 tools)");
