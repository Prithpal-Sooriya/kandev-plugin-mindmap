// Dev-only mock of the Kandev host, served by the dev server at /kandev/.
// It loads the real in-Kandev bundle (ui/bundle.js) exactly as Kandev does:
// window.registerKandevPlugin + a PluginHostApi whose React/jsx come from the
// host (here React 19). host.api.fetch goes to the dev server's mock webhooks,
// so the session → loopback WS → long-poll webhook fallback path runs for real.
import * as React from "react";
import { createRoot } from "react-dom/client";
import type { PluginHostApi } from "@kandev/plugin-sdk";

type Route = { path: string; component: (p: unknown) => unknown };
const routes: Route[] = [];
const theme = new URLSearchParams(location.search).get("theme") === "light" ? "light" : "dark";
document.documentElement.classList.toggle("dark", theme === "dark");

const toast = Object.assign((m: string) => console.info("toast", m), {
  success: (m: string) => console.info("toast", m),
  error: (m: string) => console.warn("toast", m),
  warning: (m: string) => console.warn("toast", m),
  info: (m: string) => console.info("toast", m),
  dismiss: () => {},
});

const host = {
  pluginId: "kandev-plugin-mindmap",
  React,
  jsx: React.createElement,
  api: {
    baseUrl: "",
    fetch: (path: string, init?: RequestInit) => fetch(`/api/plugins/kandev-plugin-mindmap${path}`, init),
  },
  theme,
  onThemeChange: () => () => {},
  navigate: (href: string, o?: { replace?: boolean }) => (o?.replace ? history.replaceState(null, "", href) : history.pushState(null, "", href)),
  toast,
  context: { getActiveWorkspaceId: () => "ws-demo" },
  conversation: { useSessionMessages: () => ({ messages: [] }) },
} as unknown as PluginHostApi;

(window as unknown as { registerKandevPlugin: unknown }).registerKandevPlugin = (_id: string, plugin: { initialize(r: unknown, h: PluginHostApi): void }) => {
  plugin.initialize(
    {
      registerRoute: (path: string, component: Route["component"]) => routes.push({ path, component }),
      registerNavItem: () => {},
    },
    host,
  );
};

await import(/* @vite-ignore */ `${location.origin}/ui/bundle.js`);
const route = routes.find((r) => r.path === "/mindmap");
createRoot(document.getElementById("root")!).render(
  React.createElement("div", { style: { height: "100vh", display: "flex", flexDirection: "column" } }, route ? React.createElement(route.component as never) : "plugin did not register /mindmap"),
);
