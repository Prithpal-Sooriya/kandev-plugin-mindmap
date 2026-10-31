// In-Kandev host: the `session` webhook issues a local token and the loopback
// base URL; ops go over ws://127.0.0.1:<port>; when the loopback is blocked,
// everything goes through the authenticated `sync`/`submit`/`api` webhooks;
// images always come through the `asset` webhook (desktop CSP blocks
// loopback images).
import type { PluginHostApi } from "@kandev/plugin-sdk";
import type { OutputLine, Participant, Run, SessionResponse } from "../contract";
import type { HostAdapter, Theme } from "../host/adapter";
import { isNetworkError, jsonInit, mapApi, readJson } from "../host/http";
import type { MapEndpoints, WsLike } from "../transport/connection";

const ROUTE = "/mindmap";
const REFRESH_MARGIN_MS = 5 * 60_000;

export function kandevAdapter(host: PluginHostApi): HostAdapter {
  let session: SessionResponse | null = null;
  let pending: Promise<SessionResponse> | null = null;
  /** Once a loopback call fails at the network level, stop trying it for API calls. */
  let loopbackBlocked = false;

  const webhook = (key: string, query = "", init?: RequestInit) => host.api.fetch(`/webhooks/${key}${query ? `?${query}` : ""}`, init);

  const getSession = async (force = false): Promise<SessionResponse> => {
    if (!force && session && Date.parse(session.expires_at) - Date.now() > REFRESH_MARGIN_MS) return session;
    if (!pending) {
      pending = webhook("session", "", jsonInit("POST", {}))
        .then((r) => readJson<SessionResponse>(r))
        .then((s) => (session = s))
        .finally(() => (pending = null));
    }
    return pending;
  };

  const loopbackBase = (s: SessionResponse) => `${s.base_url.replace(/\/$/, "")}/m/${encodeURIComponent(s.token)}`;

  const call = async <T>(method: string, path: string, body?: unknown): Promise<T> => {
    const s = await getSession();
    if (!loopbackBlocked) {
      try {
        return await readJson<T>(await fetch(`${loopbackBase(s)}/api${path}`, { ...jsonInit(method, body), mode: "cors" }));
      } catch (e) {
        if (!isNetworkError(e)) throw e;
        loopbackBlocked = true;
      }
    }
    // Fallback: the `api` webhook proxies one /api/* call (protocol §3).
    return readJson<T>(await webhook("api", "", jsonInit("POST", { method, path: `/api${path}`, ...(body !== undefined ? { body } : {}) })));
  };

  const endpoints: MapEndpoints = {
    wsUrl: async (mapId) => {
      const s = await getSession();
      return `${loopbackBase(s).replace(/^http/, "ws")}/ws?map_id=${encodeURIComponent(mapId)}`;
    },
    sync: async (mapId, cursor, waitS, subscribe, signal, clientId) => {
      const q = new URLSearchParams({ map_id: mapId, cursor, wait: String(waitS), client_id: clientId });
      if (subscribe.length) q.set("subscribe", subscribe.join(","));
      return readJson(await webhook("sync", q.toString(), { signal }));
    },
    submit: async (mapId, req) => readJson(await webhook("submit", new URLSearchParams({ map_id: mapId }).toString(), jsonInit("POST", req))),
  };

  const assetCache = new Map<string, Promise<string>>();

  return {
    kind: "kandev",
    canListMaps: true,
    workspaceId: () => host.context.getActiveWorkspaceId(),
    theme: () => host.theme as Theme,
    onThemeChange: (fn) => host.onThemeChange(fn),
    toast: (msg, kind) => (kind === "error" ? host.toast.error(msg) : kind === "success" ? host.toast.success(msg) : host.toast.info(msg)),
    navigate: (path) => host.navigate(path),
    initialMapId: () => new URLSearchParams(location.search).get("map"),
    setMapInUrl: (mapId) => host.navigate(mapId ? `${ROUTE}?map=${encodeURIComponent(mapId)}` : ROUTE, { replace: true }),
    // `session` without map_id returns a participant with an empty id; the real one arrives in `welcome`.
    participant: () => (session?.participant.id ? (session.participant as Participant) : null),
    api: mapApi(call),
    endpoints,
    ws: (url) => new WebSocket(url) as unknown as WsLike,
    assetUrl: (mapId, assetId) => {
      const key = `${mapId}/${assetId}`;
      let p = assetCache.get(key);
      if (!p) {
        p = webhook("asset", new URLSearchParams({ map_id: mapId, id: assetId }).toString()).then(async (r) => {
          if (!r.ok) throw new Error(`asset ${r.status}`);
          return URL.createObjectURL(await r.blob());
        });
        p.catch(() => assetCache.delete(key));
        assetCache.set(key, p);
      }
      return p;
    },
    // Kandev can read the run's session directly (the plugin WS `output` stream is the guest path).
    useLiveOutput: (run: Run | null): OutputLine[] | null => {
      const st = host.conversation.useSessionMessages({
        sessionId: run?.session_id ?? null, taskId: run?.task_id ?? null, authorTypes: ["agent"], sort: "desc", pageSize: 20,
      });
      if (!run?.session_id) return null;
      return [...st.messages].reverse().map((m) => ({ ts: m.createdAt, kind: m.type === "thinking" ? "thinking" : "text", text: m.content }));
    },
  };
}
