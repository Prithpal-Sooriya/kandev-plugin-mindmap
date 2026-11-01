// Guest web app host: served by the plugin at /m/<token>/ (share token for
// guests; a local token also works for the host). All calls are relative to
// that path, so the same build works on loopback and through the tunnel.
import { parseToken, type Participant } from "../contract";
import type { HostAdapter, Theme, ToastKind } from "../host/adapter";
import { jsonInit, mapApi, readBlob, readJson } from "../host/http";
import type { MapEndpoints, WsLike } from "../transport/connection";

export interface Toast {
  id: number;
  text: string;
  kind: ToastKind;
}

/** Tiny toast store rendered by the standalone shell. */
export class Toasts {
  list: Toast[] = [];
  version = 0;
  private seq = 0;
  private listeners = new Set<() => void>();
  subscribe(fn: () => void) {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }
  push(text: string, kind: ToastKind = "info") {
    const t = { id: ++this.seq, text, kind };
    this.list = [...this.list.slice(-3), t];
    this.emit();
    setTimeout(() => this.dismiss(t.id), kind === "error" ? 6000 : 3500);
  }
  dismiss(id: number) {
    this.list = this.list.filter((t) => t.id !== id);
    this.emit();
  }
  private emit() {
    this.version++;
    for (const fn of this.listeners) fn();
  }
}

export function tokenFromPath(path: string): string | null {
  const m = /^\/m\/([^/]+)\/?/.exec(path);
  return m ? decodeURIComponent(m[1]!) : null;
}

export function standaloneAdapter(loc: Location = location): { adapter: HostAdapter; toasts: Toasts } {
  const token = tokenFromPath(loc.pathname) ?? "";
  const base = `${loc.origin}/m/${encodeURIComponent(token)}`;
  const kind = parseToken(token)?.kind;
  const isShare = kind === "mms";
  const toasts = new Toasts();
  let participant: Participant | null = null;

  const call = async <T>(method: string, path: string, body?: unknown): Promise<T> =>
    readJson<T>(await fetch(`${base}/api${path}`, { ...jsonInit(method, body), credentials: "same-origin" }));

  const blob = async (path: string): Promise<Blob> =>
    readBlob(await fetch(`${base}/api${path}`, { headers: jsonInit("GET").headers, credentials: "same-origin" }));

  const endpoints: MapEndpoints = {
    wsUrl: async (mapId) => `${base.replace(/^http/, "ws")}/ws?map_id=${encodeURIComponent(mapId)}`,
    sync: async (mapId, cursor, waitS, subscribe, signal, clientId) => {
      const q = new URLSearchParams({ cursor, wait: String(waitS), client_id: clientId });
      if (subscribe.length) q.set("subscribe", subscribe.join(","));
      return readJson(await fetch(`${base}/api/maps/${encodeURIComponent(mapId)}/sync?${q}`, { signal, credentials: "same-origin" }));
    },
    submit: async (mapId, req) => call("POST", `/maps/${encodeURIComponent(mapId)}/ops`, req),
  };

  const themeListeners = new Set<(t: Theme) => void>();
  const media = typeof matchMedia !== "undefined" ? matchMedia("(prefers-color-scheme: dark)") : null;
  const stored = () => {
    try {
      return localStorage.getItem("mm-theme") as Theme | null;
    } catch {
      return null;
    }
  };
  let theme: Theme = stored() ?? (media?.matches ? "dark" : "light");
  media?.addEventListener?.("change", (e) => {
    if (stored()) return;
    theme = e.matches ? "dark" : "light";
    for (const fn of themeListeners) fn(theme);
  });

  const adapter: HostAdapter & { setTheme(t: Theme): void } = {
    kind: "standalone",
    canListMaps: !isShare,
    workspaceId: () => undefined,
    theme: () => theme,
    onThemeChange: (fn) => {
      themeListeners.add(fn);
      return () => themeListeners.delete(fn);
    },
    setTheme(t: Theme) {
      theme = t;
      try {
        localStorage.setItem("mm-theme", t);
      } catch {
        // ignore
      }
      for (const fn of themeListeners) fn(t);
    },
    toast: (text, k) => toasts.push(text, k),
    navigate: (path) => history.pushState(null, "", path),
    initialMapId: () => new URLSearchParams(loc.search).get("map"),
    setMapInUrl: (mapId) => {
      const u = new URL(loc.href);
      if (mapId) u.searchParams.set("map", mapId);
      else u.searchParams.delete("map");
      u.searchParams.delete("seed");
      history.replaceState(null, "", u);
    },
    participant: () => participant,
    joinGuest: async (g) => {
      const pt = await call<Participant>("POST", "/join", g);
      participant = pt;
      return pt;
    },
    guestIdentity: isShare
      ? () => {
          try {
            const g = JSON.parse(localStorage.getItem("mm-guest") ?? "null");
            return g && typeof g.name === "string" ? g : null;
          } catch {
            return null;
          }
        }
      : undefined,
    api: mapApi(call, blob),
    endpoints,
    ws: (url) => new WebSocket(url) as unknown as WsLike,
    assetUrl: async (mapId, assetId) => `${base}/assets/${encodeURIComponent(assetId)}?map_id=${encodeURIComponent(mapId)}`,
  };
  return { adapter, toasts };
}
