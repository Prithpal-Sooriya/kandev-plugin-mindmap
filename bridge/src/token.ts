// Token discovery and parsing. Order (docs/protocol.md §8.3):
//   1. MINDMAP_TOKEN env var
//   2. config file (~/.config/kandev-mindmap/config.json, {"token": "..."}; MINDMAP_CONFIG overrides the path)
//   3. a per-call `run_token` argument (handled by the caller, passed in as `runToken`)
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

export type TokenSource = "env" | "config" | "arg";

export interface ResolvedToken {
  token: string;
  source: TokenSource;
  kind: "run" | "personal";
  port: number;
}

export class TokenError extends Error {
  constructor(
    readonly code: "unauthorized" | "bad_request",
    message: string,
  ) {
    super(message);
  }
}

const SECRET = /^[A-Za-z0-9_-]{43}$/;

/** Parses `mmr1.<port>.<secret>` or `mmp1.<port>.<secret>`. Returns an explanation instead on failure. */
export function parseBridgeToken(raw: string): { ok: true; kind: "run" | "personal"; port: number } | { ok: false; reason: string } {
  const parts = raw.trim().split(".");
  const prefix = parts[0] ?? "";
  if (prefix === "mml1" || prefix === "mms1" || prefix === "mmg1") {
    return { ok: false, reason: `${prefix}. tokens are for the canvas UI, not the agent bridge; use a run token (mmr1.) or a personal token (mmp1.)` };
  }
  if (prefix !== "mmr1" && prefix !== "mmp1") {
    return { ok: false, reason: "expected a run token (mmr1.<port>.<secret>) or personal token (mmp1.<port>.<secret>)" };
  }
  if (parts.length !== 3) {
    return { ok: false, reason: `${prefix}. tokens look like ${prefix}.<port>.<secret>` };
  }
  const port = Number(parts[1]);
  if (!/^\d+$/.test(parts[1]!) || port < 1 || port > 65535) {
    return { ok: false, reason: "the port part of the token is not a valid port number" };
  }
  if (!SECRET.test(parts[2]!)) {
    return { ok: false, reason: "the secret part of the token is malformed (expected 43 base64url characters)" };
  }
  return { ok: true, kind: prefix === "mmr1" ? "run" : "personal", port };
}

export function configPath(env: NodeJS.ProcessEnv = process.env): string {
  return env.MINDMAP_CONFIG || join(homedir(), ".config", "kandev-mindmap", "config.json");
}

function readConfigToken(env: NodeJS.ProcessEnv): string | undefined {
  const path = configPath(env);
  let text: string;
  try {
    text = readFileSync(path, "utf8");
  } catch {
    return undefined; // missing config is normal
  }
  try {
    const token = (JSON.parse(text) as { token?: unknown }).token;
    if (typeof token === "string" && token.trim()) return token.trim();
    return undefined;
  } catch {
    throw new TokenError("bad_request", `${path} is not valid JSON. Expected {"token": "mmp1.<port>.<secret>"}.`);
  }
}

/** The raw configured token (env, then config file), without the per-call argument. */
export function configuredToken(env: NodeJS.ProcessEnv = process.env): { token: string; source: "env" | "config" } | undefined {
  const fromEnv = env.MINDMAP_TOKEN?.trim();
  if (fromEnv) return { token: fromEnv, source: "env" };
  const fromFile = readConfigToken(env);
  if (fromFile) return { token: fromFile, source: "config" };
  return undefined;
}

/** True when the bridge has its own token, so tool schemas need no `run_token` property. */
export function hasConfiguredToken(env: NodeJS.ProcessEnv = process.env): boolean {
  try {
    return configuredToken(env) !== undefined;
  } catch {
    return false; // a broken config file: surface the error at call time, ask for run_token meanwhile
  }
}

/** Resolves the token for one call, in protocol order. Throws TokenError with an actionable message. */
export function resolveToken(runToken: string | undefined, env: NodeJS.ProcessEnv = process.env): ResolvedToken {
  const configured = configuredToken(env);
  const picked = configured ?? (runToken?.trim() ? { token: runToken.trim(), source: "arg" as const } : undefined);
  if (!picked) {
    throw new TokenError(
      "unauthorized",
      "no Mind Map token. Set MINDMAP_TOKEN, put {\"token\": \"mmp1....\"} in " +
        `${configPath(env)}, or pass the run_token from your launch prompt.`,
    );
  }
  const parsed = parseBridgeToken(picked.token);
  if (!parsed.ok) {
    throw new TokenError("unauthorized", `invalid token from ${picked.source}: ${parsed.reason}.`);
  }
  return { token: picked.token, source: picked.source, kind: parsed.kind, port: parsed.port };
}

export function baseUrlFor(port: number, env: NodeJS.ProcessEnv = process.env): string {
  const override = env.MINDMAP_URL?.trim();
  return (override || `http://127.0.0.1:${port}`).replace(/\/+$/, "");
}
