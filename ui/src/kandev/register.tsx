// In-Kandev bundle entry: registers /mindmap and a main nav item. Uses the
// host's React through the registry API: no bundled React, no react-dom.
import type { PluginHostApi, PluginRegistry } from "@kandev/plugin-sdk";
import { App } from "../canvas/App";
import { MindmapIcon } from "../canvas/icons";
import type { HostAdapter } from "../host/adapter";
import { bindReact, h } from "../runtime";
import { kandevAdapter } from "./adapter";

const PLUGIN_ID = "kandev-plugin-mindmap";
const ROUTE = "/mindmap";

let adapter: HostAdapter | null = null;

function MindmapPage() {
  return h(App, { host: adapter! });
}

window.registerKandevPlugin(PLUGIN_ID, {
  initialize(registry: PluginRegistry, api: PluginHostApi) {
    bindReact(api.React, api.jsx);
    adapter = kandevAdapter(api);
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
