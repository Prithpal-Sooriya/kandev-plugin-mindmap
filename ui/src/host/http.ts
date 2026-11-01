// Shared HTTP helpers for both hosts: JSON calls with contract errors, and the
// MapApi built on top of a `/api/*` caller.
import {
  CONTRACT_HEADER, CONTRACT_VERSION, ContractError, type ActivityEvent, type AgentProfile, type DocTree, type ErrorCode, type MapSummary,
  type MindMap, type Op, type Proposal, type RepoInfo, type Run, type ShareLink, type SuggestResponse, type TaskRunInfo,
} from "../contract";
import type { MapApi } from "./adapter";

/** Calls one `/api/*` route of protocol §4. `path` starts after `/api`, e.g. `/maps`. */
export type ApiCall = <T>(method: string, path: string, body?: unknown) => Promise<T>;
/** GETs one `/api/*` route as a file. */
export type BlobCall = (path: string) => Promise<Blob>;

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

/** A file response, or the route's contract error. */
export async function readBlob(res: Response): Promise<Blob> {
  if (!res.ok) await readJson(res);
  return res.blob();
}

export function jsonInit(method: string, body?: unknown): RequestInit {
  return {
    method,
    headers: { "Content-Type": "application/json", [CONTRACT_HEADER]: String(CONTRACT_VERSION) },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  };
}

export function mapApi(call: ApiCall, blob: BlobCall): MapApi {
  const m = (mapId: string) => `/maps/${encodeURIComponent(mapId)}`;
  const docQuery = (id?: string) => (id ? `?node_id=${encodeURIComponent(id)}` : "");
  return {
    listMaps: () => call<MapSummary[]>("GET", "/maps"),
    createMap: (req) => call<MindMap>("POST", "/maps", req),
    profiles: () => call<AgentProfile[]>("GET", "/profiles"),
    reviewProposal: (id, req) => call<Proposal>("POST", `/proposals/${encodeURIComponent(id)}/review`, req),
    startRun: async (mapId, req) =>
      (await call<{ runs: Run[] }>("POST", `/maps/${encodeURIComponent(mapId)}/runs`, req)).runs,
    startSwarm: async (mapId, req) =>
      (await call<{ runs: Run[] }>("POST", `/maps/${encodeURIComponent(mapId)}/swarms`, req)).runs,
    compile: async (mapId, req) => (await call<{ runs: Run[] }>("POST", `${m(mapId)}/compile`, req)).runs,
    suggest: (mapId, req) => call<SuggestResponse>("POST", `${m(mapId)}/suggest`, req),
    suggestJob: (mapId, jobId) => call<SuggestResponse>("GET", `${m(mapId)}/suggest/${encodeURIComponent(jobId)}`),
    approveRun: async (runId) => (await call<{ runs: Run[] }>("POST", `/runs/${encodeURIComponent(runId)}/approve`, {})).runs,
    docs: (mapId) => call<DocTree[]>("GET", `${m(mapId)}/docs`),
    exportZip: (mapId, id) => blob(`${m(mapId)}/export.zip${docQuery(id)}`),
    exportDocs: (mapId, id) => call<DocTree>("POST", `${m(mapId)}/export${docQuery(id)}`, {}),
    stopRun: (runId) => call<void>("POST", `/runs/${encodeURIComponent(runId)}/stop`),
    messageRun: (runId, text) => call<void>("POST", `/runs/${encodeURIComponent(runId)}/message`, { text }),
    answerQuestion: async (mapId, nodeId, answer) => {
      await call<unknown>("POST", `/maps/${encodeURIComponent(mapId)}/nodes/${encodeURIComponent(nodeId)}/answer`, { answer });
    },
    repos: (mapId) => call<RepoInfo[]>("GET", `/repos?map_id=${encodeURIComponent(mapId)}`),
    taskRun: (taskId) => call<TaskRunInfo>("GET", `/tasks/${encodeURIComponent(taskId)}/run`),
    runTasks: async () => (await call<{ task_ids: string[] }>("GET", "/runs/tasks")).task_ids,
    undo: (mapId) => call<{ ops: Op[]; rev: number }>("POST", `/maps/${encodeURIComponent(mapId)}/undo`, {}),
    getOps: (mapId, from, to) => call<{ ops: Op[] }>("GET", `/maps/${encodeURIComponent(mapId)}/ops?from=${from}&to=${to}`),
    forkMap: (mapId, rev) => call<MindMap>("POST", `/maps/${encodeURIComponent(mapId)}/fork`, { rev }),
    shareStatus: (mapId) => call<ShareLink>("GET", `/maps/${encodeURIComponent(mapId)}/share`),
    startShare: (mapId) => call<ShareLink>("POST", `/maps/${encodeURIComponent(mapId)}/share`, {}),
    rotateShare: (mapId) => call<ShareLink>("POST", `/maps/${encodeURIComponent(mapId)}/share/rotate`, {}),
    stopShare: (mapId) => call<void>("DELETE", `/maps/${encodeURIComponent(mapId)}/share`),
    killSharing: () => call<void>("POST", "/kill", {}),
    audit: (mapId) => call<ActivityEvent[]>("GET", `/maps/${encodeURIComponent(mapId)}/audit`),
  };
}

/** True for errors that mean "the network path is blocked", as opposed to a server answer. */
export function isNetworkError(e: unknown): boolean {
  return e instanceof TypeError || (e instanceof DOMException && e.name !== "AbortError");
}
