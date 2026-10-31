// Standalone guest web app: bundles its own React 19 + react-dom and is
// served by the plugin at /m/<token>/ (go:embed of server/web/dist).
import * as ReactLib from "react";
import { createRoot } from "react-dom/client";
import type { HostReact } from "@kandev/plugin-sdk";
import { App } from "../canvas/App";
import { useStore } from "../canvas/hooks";
import { Icon, ICONS } from "../canvas/icons";
import { bindReact, h } from "../runtime";
import { standaloneAdapter, type Toasts } from "./adapter";
import "./app.css";

bindReact(ReactLib as unknown as HostReact);

const { adapter, toasts } = standaloneAdapter();

function ToastHost(props: { toasts: Toasts }) {
  useStore(props.toasts);
  return (
    <div className="mm-toasts" role="status" aria-live="polite">
      {props.toasts.list.map((t) => (
        <div key={t.id} className={`mm-toast mm-toast-${t.kind}`} onClick={() => props.toasts.dismiss(t.id)}>{t.text}</div>
      ))}
    </div>
  );
}

function Shell() {
  const [theme, setTheme] = ReactLib.useState(adapter.theme());
  ReactLib.useEffect(() => adapter.onThemeChange(setTheme), []);
  ReactLib.useEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark");
  }, [theme]);
  const set = (adapter as unknown as { setTheme(t: string): void }).setTheme;
  return (
    <div className="mm-standalone">
      <App host={adapter} />
      <button type="button" className="mm-icon-btn mm-theme-toggle" onClick={() => set(theme === "dark" ? "light" : "dark")} aria-label="Toggle dark mode">
        <Icon d={theme === "dark" ? ICONS.sun : ICONS.moon} />
      </button>
      <ToastHost toasts={toasts} />
    </div>
  );
}

createRoot(document.getElementById("root")!).render(h(Shell, null));
