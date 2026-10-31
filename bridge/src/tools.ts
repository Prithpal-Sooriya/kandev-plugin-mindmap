// The 13 tool declarations, embedded at build time from contract/tools/*.json.
// contract/tools is the source of truth; never copy schemas by hand.
import context from "../../contract/tools/context.json";
import read from "../../contract/tools/read.json";
import search from "../../contract/tools/search.json";
import add from "../../contract/tools/add.json";
import update from "../../contract/tools/update.json";
import attach from "../../contract/tools/attach.json";
import restructure from "../../contract/tools/restructure.json";
import link from "../../contract/tools/link.json";
import claim from "../../contract/tools/claim.json";
import spawn from "../../contract/tools/spawn.json";
import ask_human from "../../contract/tools/ask_human.json";
import write_doc from "../../contract/tools/write_doc.json";
import report from "../../contract/tools/report.json";

export interface ToolDecl {
  name: string;
  description: string;
  annotations?: {
    read_only_hint?: boolean;
    destructive_hint?: boolean;
    idempotent_hint?: boolean;
    open_world_hint?: boolean;
  };
  input_schema: Record<string, unknown> & { properties?: Record<string, unknown> };
}

/** Manifest order; matches contract.ToolNames in Go. */
export const TOOLS: readonly ToolDecl[] = [
  context, read, search, add, update, attach, restructure, link, claim, spawn, ask_human, write_doc, report,
] as ToolDecl[];

/** Bridge-side prefix: the backend calls them `read`, the bridge exposes `mindmap_read`. */
export const TOOL_PREFIX = "mindmap_";

export const RUN_TOKEN_DESCRIPTION =
  "Your Mind Map run token (mmr1....) from the launch prompt. Pass it on every call. " +
  "It is removed before the call is made and is never stored.";

/** Schema for the model: a deep copy, plus `run_token` only when no token is configured. */
export function schemaFor(decl: ToolDecl, withRunToken: boolean): Record<string, unknown> {
  const schema = structuredClone(decl.input_schema);
  if (withRunToken) {
    schema.properties = {
      ...(schema.properties ?? {}),
      run_token: { type: "string", maxLength: 300, description: RUN_TOKEN_DESCRIPTION },
    };
  }
  return schema;
}

export function byBridgeName(bridgeName: string): ToolDecl | undefined {
  if (!bridgeName.startsWith(TOOL_PREFIX)) return undefined;
  const name = bridgeName.slice(TOOL_PREFIX.length);
  return TOOLS.find((t) => t.name === name);
}
