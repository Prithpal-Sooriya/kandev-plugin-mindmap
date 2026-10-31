// Shared HTTP helpers for both hosts: JSON calls with contract errors, and the
// MapApi built on top of a `/api/*` caller.
import {
  CONTRACT_HEADER, CONTRACT_VERSION, ContractError, type AgentProfile, type ErrorCode, type MapSummary, type MindMap,
  type Op, type Proposal, type Run,
} from "../contract";
import type { MapApi } from "./adapter";

/** Calls one `/api/*` route of protocol §4. `path` starts after `/api`, e.g. `/maps`. */
export type ApiCall = <T>(method: string, path: string, body?: unknown) => Promise<T>;

export async function readJson<T>(res: Response): Promise<T> {
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let body: unknown = undefined;
  try {
    body = text ? JSON.parse(text) : undefined;
  } catch {
    // fall through to the status check
  }
  if (!res.ok) {
    const err = (body as { error?: { code: ErrorCode; message: string; field?: string; retry_ms?: number } })?.error;
    if (err) throw new ContractError(err.code, err.message, err.field, err.retry_ms);
    throw new ContractError(res.status === 404 ? "not_found" : "unavailable", `HTTP ${res.status}`);
  }
  return body as T;
}

export function jsonInit(method: string, body?: unknown): RequestInit {
  return {
    method,
    headers: { "Content-Type": "application/json", [CONTRACT_HEADER]: String(CONTRACT_VERSION) },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  };
}

export function mapApi(call: ApiCall): MapApi {
  return {
    listMaps: () => call<MapSummary[]>("GET", "/maps"),
    createMap: (req) => call<MindMap>("POST", "/maps", req),
    profiles: () => call<AgentProfile[]>("GET", "/profiles"),
    reviewProposal: (id, req) => call<Proposal>("POST", `/proposals/${encodeURIComponent(id)}/review`, req),
    startRun: async (mapId, req) =>
      (await call<{ runs: Run[] }>("POST", `/maps/${encodeURIComponent(mapId)}/runs`, req)).runs,
    undo: (mapId) => call<{ ops: Op[]; rev: number }>("POST", `/maps/${encodeURIComponent(mapId)}/undo`, {}),
    getOps: (mapId, from, to) => call<{ ops: Op[] }>("GET", `/maps/${encodeURIComponent(mapId)}/ops?from=${from}&to=${to}`),
    forkMap: (mapId, rev) => call<MindMap>("POST", `/maps/${encodeURIComponent(mapId)}/fork`, { rev }),
  };
}

/** True for errors that mean "the network path is blocked", as opposed to a server answer. */
export function isNetworkError(e: unknown): boolean {
  return e instanceof TypeError || (e instanceof DOMException && e.name !== "AbortError");
}
