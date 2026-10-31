// Phase 0 hello page: registers /mindmap and a main nav item, and probes whether
// the in-Kandev UI can reach the plugin's own loopback HTTP + WebSocket server.
import type { PluginHostApi, PluginRegistry } from "@kandev/plugin-sdk";
import { bindHost, h, host, React } from "./runtime";

const PLUGIN_ID = "kandev-plugin-mindmap";
const ROUTE = "/mindmap";

type Probe = {
  listen_addr?: string;
  port?: number;
  ws_url?: string;
  hello_url?: string;
  listen_error?: string;
};

type Status = { state: "idle" | "connecting" | "open" | "error"; detail: string };

function MindmapIcon(props: { className?: string; size?: number }) {
  const size = props.size ?? 16;
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={props.className}
      aria-hidden="true"
    >
      <circle cx="12" cy="12" r="3" />
      <circle cx="4" cy="5" r="2" />
      <circle cx="20" cy="5" r="2" />
      <circle cx="4" cy="19" r="2" />
      <circle cx="20" cy="19" r="2" />
      <path d="M10 10L5.5 6.5M14 10l4.5-3.5M10 14l-4.5 3.5M14 14l4.5 3.5" />
    </svg>
  );
}

// Run the probe sequence once: ask the backend for its loopback address, then
// try fetch (HTTP) and WebSocket against it. Results are mirrored onto
// window.__mindmapProbe so a headless check can read them.
function useLoopbackProbe() {
  const [probe, setProbe] = React.useState(null as Probe | null);
  const [http, setHttp] = React.useState({ state: "idle", detail: "" } as Status);
  const [ws, setWs] = React.useState({ state: "idle", detail: "" } as Status);

  React.useEffect(() => {
    let socket: WebSocket | null = null;
    let cancelled = false;
    const publish = (patch: Record<string, unknown>) => {
      const w = window as unknown as { __mindmapProbe?: Record<string, unknown> };
      w.__mindmapProbe = { ...(w.__mindmapProbe ?? {}), ...patch };
    };
    (async () => {
      try {
        const res = await host.api.fetch("/webhooks/probe");
        const body = (await res.json()) as Probe;
        if (cancelled) return;
        setProbe(body);
        publish({ probe: body, origin: window.location.origin });
        if (!body.port) {
          setWs({ state: "error", detail: body.listen_error ?? "no port" });
          return;
        }
        setHttp({ state: "connecting", detail: "" });
        try {
          const r = await fetch(body.hello_url as string, { mode: "cors" });
          const text = (await r.text()).trim();
          setHttp({ state: "open", detail: `${r.status} ${text}` });
          publish({ http: `ok ${r.status} ${text}` });
        } catch (e) {
          setHttp({ state: "error", detail: String(e) });
          publish({ http: `error ${String(e)}` });
        }
        setWs({ state: "connecting", detail: body.ws_url as string });
        socket = new WebSocket(body.ws_url as string);
        socket.onopen = () => socket?.send("ping-from-ui");
        socket.onmessage = (ev) => {
          setWs({ state: "open", detail: String(ev.data) });
          publish({ ws: `open ${String(ev.data)}` });
        };
        socket.onerror = () => {
          setWs({ state: "error", detail: "WebSocket error (blocked or refused)" });
          publish({ ws: "error" });
        };
      } catch (e) {
        setWs({ state: "error", detail: String(e) });
        publish({ ws: `error ${String(e)}` });
      }
    })();
    return () => {
      cancelled = true;
      socket?.close();
    };
  }, []);

  return { probe, http, ws };
}

function MindmapPage() {
  const { probe, http, ws } = useLoopbackProbe();
  return (
    <div className="mm-page" data-testid="mindmap-hello">
      <h2>Mind Map</h2>
      <p>Phase 0 spike. Hello from the plugin UI.</p>
      <ul>
        <li data-testid="mm-listen">Plugin server: {probe?.listen_addr ?? probe?.listen_error ?? "asking backend..."}</li>
        <li data-testid="mm-http">HTTP to loopback: {http.state} {http.detail}</li>
        <li data-testid="mm-ws">WebSocket to loopback: {ws.state} {ws.detail}</li>
      </ul>
    </div>
  );
}

window.registerKandevPlugin(PLUGIN_ID, {
  initialize(registry: PluginRegistry, api: PluginHostApi) {
    bindHost(api);
    registry.registerRoute(ROUTE, MindmapPage, {
      topbar: { title: "Mind Map", subtitle: "Agentic, multiplayer", icon: MindmapIcon },
    });
    registry.registerNavItem({ id: "mindmap", label: "Mind Map", path: ROUTE, icon: MindmapIcon, section: "main" });
  },
});

declare global {
  interface Window {
    registerKandevPlugin(
      id: string,
      plugin: { initialize(registry: PluginRegistry, host: PluginHostApi): void; destroy?(): void },
    ): void;
  }
}
