// Shared bridge client: resolves the token, calls POST /agent/v1/tools/<name>, maps every failure to
// clear model-readable text. `callTool` never throws; failures come back as `is_error` results.
import { readFileSync, statSync } from "node:fs";
import { basename, extname, isAbsolute, resolve } from "node:path";
import { resolveToken, baseUrlFor, TokenError } from "./token.js";

export interface ToolResult {
  text: string;
  structured?: Record<string, unknown>;
  is_error?: boolean;
}

export interface CallOptions {
  env?: NodeJS.ProcessEnv;
  /** Working directory for resolving relative `attach.file_path`. */
  cwd?: string;
  fetch?: typeof fetch;
  /** Client-side timeout. The server gives up at 25 s, so the default leaves a little margin. */
  timeoutMs?: number;
  signal?: AbortSignal;
}

export const CONTRACT_VERSION = "1";
export const DEFAULT_TIMEOUT_MS = 28_000;
const MAX_ATTACH_BYTES = 10 * 1024 * 1024;

const MIME_BY_EXT: Record<string, string> = {
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".gif": "image/gif",
  ".webp": "image/webp",
  ".svg": "image/svg+xml",
  ".pdf": "application/pdf",
  ".txt": "text/plain",
  ".md": "text/markdown",
  ".csv": "text/csv",
  ".json": "application/json",
};

export function errorResult(code: string, message: string, extra?: Record<string, unknown>): ToolResult {
  return {
    text: `error ${code}: ${message}`,
    structured: { error: { code, message, ...extra } },
    is_error: true,
  };
}

/** Maps a non-200 response (or its parsed `{error}` body) to an error result. */
export function mapHttpError(status: number, bodyText: string, baseUrl: string): ToolResult {
  let body: { error?: { code?: string; message?: string; field?: string; retry_ms?: number } } | undefined;
  try {
    body = JSON.parse(bodyText);
  } catch {
    body = undefined;
  }
  const server = body?.error;
  switch (status) {
    case 401:
      return errorResult(
        "unauthorized",
        "the plugin rejected the token (missing, expired or revoked). Run tokens end 10 minutes after the run finishes; " +
          "create a new personal token in the Mind Map plugin settings, or ask for a new run.",
      );
    case 404:
      return errorResult("not_found", `${server?.message ?? "no such tool or route"}. Is the plugin up to date? (${baseUrl})`);
    case 413:
      return errorResult("too_large", "the request body is over 16 MiB. Send less data (for attach, use a smaller file).");
    case 426:
      return errorResult(
        "version",
        "the plugin does not support this bridge's contract version. Update the bridge or the plugin so both speak contract 1.",
      );
    case 429:
      return errorResult("rate_limited", `${server?.message ?? "slow down"}${server?.retry_ms ? `; retry in ${server.retry_ms} ms` : ""}.`, {
        retry_ms: server?.retry_ms,
      });
  }
  if (status >= 500) {
    return errorResult(
      server?.code ?? "unavailable",
      `${server?.message ?? `the plugin failed with HTTP ${status}`}. This is a plugin-side problem; try again, and if it persists tell the user.`,
    );
  }
  if (server?.code) return errorResult(server.code, server.message ?? `HTTP ${status}`, { field: server.field });
  return errorResult("bad_request", `unexpected HTTP ${status} from the plugin.`);
}

/** Maps a thrown fetch failure (refused, timeout, abort) to an error result. */
export function mapTransportError(err: unknown, baseUrl: string, timeoutMs: number): ToolResult {
  const e = err as { name?: string; code?: string; cause?: { code?: string } };
  const code = e.cause?.code ?? e.code;
  if (e.name === "TimeoutError") {
    return errorResult("unavailable", `the plugin did not answer within ${Math.round(timeoutMs / 1000)} s. The call may or may not have been applied: read the map before retrying a write.`);
  }
  if (e.name === "AbortError") return errorResult("unavailable", "the call was cancelled.");
  if (code === "ECONNREFUSED" || code === "ECONNRESET" || code === "UND_ERR_SOCKET") {
    return errorResult(
      "unavailable",
      `cannot reach the Mind Map plugin at ${baseUrl}. Kandev must be running with the plugin enabled. ` +
        "The port can change if the plugin restarts: if so, use a token issued after the restart.",
    );
  }
  return errorResult("unavailable", `could not call the Mind Map plugin at ${baseUrl}: ${err instanceof Error ? err.message : String(err)}`);
}

/** attach: the plugin can read only some absolute paths, so the bridge reads relative paths itself and sends base64. */
export function prepareAttach(args: Record<string, unknown>, cwd: string): { args: Record<string, unknown> } | { error: ToolResult } {
  const p = args.file_path;
  if (typeof p !== "string" || p === "" || isAbsolute(p)) return { args };
  const abs = resolve(cwd, p);
  let size: number;
  try {
    size = statSync(abs).size;
  } catch {
    return { error: errorResult("fetch_failed", `file_path: cannot read ${abs}. Give an existing file.`, { field: "file_path" }) };
  }
  if (size > MAX_ATTACH_BYTES) {
    return { error: errorResult("too_large", `file_path: ${abs} is ${size} bytes; the limit is 10 MB.`, { field: "file_path" }) };
  }
  const mime = (typeof args.mime_type === "string" && args.mime_type) || MIME_BY_EXT[extname(abs).toLowerCase()];
  if (!mime) {
    return {
      error: errorResult("unsupported_media", `file_path: cannot tell the MIME type of ${basename(abs)}; pass mime_type.`, { field: "mime_type" }),
    };
  }
  const { file_path: _drop, ...rest } = args;
  return { args: { ...rest, base64: readFileSync(abs).toString("base64"), mime_type: mime, filename: rest.filename ?? basename(abs) } };
}

/**
 * Calls one tool. `name` is the backend name (`read`, not `mindmap_read`).
 * `args.run_token` is stripped; it is used only when no token is configured.
 */
export async function callTool(name: string, args: Record<string, unknown>, opts: CallOptions = {}): Promise<ToolResult> {
  const env = opts.env ?? process.env;
  const { run_token: runToken, ...toolArgs } = args;
  let resolved;
  try {
    resolved = resolveToken(typeof runToken === "string" ? runToken : undefined, env);
  } catch (err) {
    if (err instanceof TokenError) return errorResult(err.code, err.message);
    throw err;
  }
  const baseUrl = baseUrlFor(resolved.port, env);

  let body = toolArgs;
  if (name === "attach") {
    const prepared = prepareAttach(toolArgs, opts.cwd ?? process.cwd());
    if ("error" in prepared) return prepared.error;
    body = prepared.args;
  }

  const timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;
  const signals = [AbortSignal.timeout(timeoutMs), ...(opts.signal ? [opts.signal] : [])];
  let res: Response;
  try {
    res = await (opts.fetch ?? fetch)(`${baseUrl}/agent/v1/tools/${encodeURIComponent(name)}`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${resolved.token}`,
        "X-Mindmap-Contract": CONTRACT_VERSION,
        "Content-Type": "application/json",
      },
      body: JSON.stringify(body),
      signal: AbortSignal.any(signals),
    });
  } catch (err) {
    return mapTransportError(err, baseUrl, timeoutMs);
  }

  const text = await res.text().catch(() => "");
  if (res.status !== 200) return mapHttpError(res.status, text, baseUrl);
  try {
    const result = JSON.parse(text) as ToolResult;
    if (typeof result?.text !== "string" || result.text === "") throw new Error("empty text");
    return result;
  } catch {
    return errorResult("internal", "the plugin returned a malformed tool result.");
  }
}
