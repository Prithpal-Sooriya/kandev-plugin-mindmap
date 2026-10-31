// Pi extension: registers the 13 mind map tools natively (Pi 1.x `pi.registerTool`).
// Built into a single file; `typebox` and `@earendil-works/*` are provided by Pi and stay external.
import { Type } from "typebox";
import { callTool } from "./client.js";
import { hasConfiguredToken } from "./token.js";
import { schemaFor, TOOLS, TOOL_PREFIX } from "./tools.js";

// Minimal structural type: avoids a build-time dependency on Pi's types.
interface PiApi {
  registerTool(def: {
    name: string;
    label: string;
    description: string;
    promptSnippet?: string;
    parameters: unknown;
    annotations?: Record<string, boolean | undefined>;
    execute(
      toolCallId: string,
      params: Record<string, unknown>,
      signal: AbortSignal | undefined,
      onUpdate: unknown,
      ctx: { cwd?: string },
    ): Promise<{ content: { type: "text"; text: string }[]; details: Record<string, unknown> }>;
  }): void;
}

export default function mindmapExtension(pi: PiApi): void {
  // Evaluated when Pi loads the extension. With no token configured, tools carry a `run_token` property.
  const withRunToken = !hasConfiguredToken();
  for (const t of TOOLS) {
    pi.registerTool({
      name: TOOL_PREFIX + t.name,
      label: `Mind map ${t.name}`,
      description: t.description,
      promptSnippet: t.description.split(/(?<=\.)\s/)[0],
      parameters: Type.Unsafe(schemaFor(t, withRunToken)),
      annotations: {
        readOnlyHint: t.annotations?.read_only_hint,
        destructiveHint: t.annotations?.destructive_hint,
        idempotentHint: t.annotations?.idempotent_hint,
        openWorldHint: t.annotations?.open_world_hint,
      },
      async execute(_id, params, signal, _onUpdate, ctx) {
        const result = await callTool(t.name, params ?? {}, { signal, cwd: ctx?.cwd });
        // Pi treats a thrown error as a tool error (is_error); returning text keeps the message visible either way.
        if (result.is_error) throw new Error(result.text);
        return { content: [{ type: "text", text: result.text }], details: result.structured ?? {} };
      },
    });
  }
}
