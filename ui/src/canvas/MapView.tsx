// One open map: header (title, presence, agents chip, proposals), canvas,
// inspector and the command palette.
import type { Participant } from "../contract";
import type { HostAdapter } from "../host/adapter";
import { installCanvasMeasurer } from "../layout/measure";
import { h, React } from "../runtime";
import { AvatarStack } from "./agent";
import { AgentsDock } from "./dock";
import { Canvas, type CanvasApi } from "./Canvas";
import { MapController } from "./controller";
import { useStore } from "./hooks";
import { Icon, ICONS } from "./icons";
import { Inspector } from "./Inspector";
import type { Command } from "./keys";
import { Palette, type PaletteAction } from "./Palette";
import { ShareControls } from "./ShareDialog";

function fontFamily(): string {
  const v = typeof document !== "undefined" ? getComputedStyle(document.documentElement).getPropertyValue("--font-sans").trim() : "";
  return v || 'ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif';
}

const IS_MAC = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

export function MapView(props: { host: HostAdapter; mapId: string; onBack?: () => void; guest?: { name: string; color: string } | null; dev?: boolean }) {
  const { host, mapId } = props;
  const [ctl] = React.useState(() => {
    installCanvasMeasurer(fontFamily());
    return new MapController(mapId, host, { name: props.guest?.name, color: props.guest?.color, strict: props.dev });
  });
  React.useEffect(() => {
    ctl.start();
    // Web fonts change text metrics: re-measure once they are in.
    document.fonts?.ready.then(() => ctl.relayoutAll(), () => {});
    if (props.dev) (window as unknown as { __mm?: unknown }).__mm = { ctl };
    return () => ctl.stop();
  }, [ctl]);
  useStore(ctl);
  const apiRef = React.useRef(null as CanvasApi | null);

  const onCommand = React.useCallback((c: Command) => {
    if (c.type === "palette") {
      ctl.paletteOpen = true;
      ctl.bump();
    } else if (c.type === "inspector") {
      ctl.inspectorOpen = !ctl.inspectorOpen;
      ctl.bump();
      // The canvas narrows when the inspector opens: keep the selection in view.
      const sel = ctl.selection;
      if (sel) setTimeout(() => apiRef.current?.reveal(sel), 60);
    }
  }, [ctl]);

  // ⌘K anywhere in the map view (also from inputs).
  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((IS_MAC ? e.metaKey : e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        ctl.paletteOpen = !ctl.paletteOpen;
        ctl.paletteSeed = "";
        ctl.bump();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [ctl]);

  const e = ctl.engine;
  const people = [...e.participants.values()].filter((p) => p.online !== false || p.id === e.participant?.id)
    .sort((a, b) => Number(b.id === e.participant?.id) - Number(a.id === e.participant?.id) || Number(a.kind === "agent") - Number(b.kind === "agent"));
  const runs = [...e.runs.values()];
  const running = runs.filter((r) => r.status === "running" || r.status === "queued").length;
  const needsYou = runs.filter((r) => r.status === "waiting" || r.status === "review").length;
  const proposals = [...e.proposals.values()].filter((p) => p.status === "open" || p.status === "ready");
  const pendingItems = proposals.reduce((n, p) => n + p.items.filter((i) => i.status === "pending").length, 0);

  const stub = (label: string) => () => host.toast(`${label} arrives in Phase 2.`, "info");
  const actions: PaletteAction[] = [
    { id: "run", label: "Run agent on this node…", hint: "Phase 2", run: stub("Run agent") },
    { id: "swarm", label: "Swarm this node…", hint: "Phase 2", run: stub("Swarm") },
    { id: "explore", label: "Explore from here…", hint: "Phase 2", run: stub("Explore") },
    { id: "garden", label: "Garden the map…", hint: "Phase 2", run: stub("Garden") },
    { id: "compile", label: "Compile branch to docs…", hint: "Phase 2", run: stub("Compile") },
    { id: "fit", label: "Fit map to screen", hint: IS_MAC ? "⌘0" : "Ctrl 0", run: () => apiRef.current?.fit() },
    { id: "ghosts", label: ctl.showGhosts ? "Hide proposed changes" : "Show proposed changes", run: () => ctl.toggleGhosts() },
    { id: "inspector", label: ctl.inspectorOpen ? "Close inspector" : "Open inspector", hint: "⇧Enter", run: () => { ctl.inspectorOpen = !ctl.inspectorOpen; ctl.bump(); } },
    { id: "child", label: "Add child", hint: "Tab", run: () => apiRef.current?.command({ type: "addChild" }) },
    { id: "undo", label: "Undo", hint: IS_MAC ? "⌘Z" : "Ctrl Z", run: () => ctl.undo() },
    { id: "redo", label: "Redo", hint: IS_MAC ? "⇧⌘Z" : "Ctrl Y", run: () => ctl.redo() },
  ];

  const closePalette = () => {
    ctl.paletteOpen = false;
    ctl.bump();
    apiRef.current?.focus();
  };

  return (
    <div className="mm-map" data-testid="mm-map">
      <header className="mm-header">
        {props.onBack ? <button type="button" className="mm-icon-btn" onClick={props.onBack} aria-label="All maps"><Icon d={ICONS.back} /></button> : null}
        <MapTitle ctl={ctl} />
        <ConnChip state={ctl.connState} />
        <span className="mm-spacer" />
        {running || needsYou ? (
          <button className={`mm-chip ${needsYou ? "mm-chip-waiting" : "mm-chip-running"}`} onClick={() => { ctl.dockOpen = true; ctl.bump(); }} title="Open agents dock">
            {running ? `${running} agent${running > 1 ? "s" : ""} running` : ""}{running && needsYou ? " · " : ""}{needsYou ? `${needsYou} need${needsYou > 1 ? "" : "s"} you` : ""}
          </button>
        ) : null}
        {pendingItems ? (
          <button type="button" className={`mm-btn mm-btn-sm${ctl.showGhosts ? " mm-btn-on" : ""}`} onClick={() => ctl.toggleGhosts()} title="Show or hide proposed changes">
            <Icon d={ICONS.ghost} /> {pendingItems} proposed
          </button>
        ) : null}
        <AvatarStack people={people as Participant[]} max={6} />
        <ShareControls host={host} mapId={mapId} guests={[...e.participants.values()].filter((p) => p.kind === "human" && !p.is_host).length} />
        <button type="button" className="mm-btn mm-btn-sm" onClick={() => onCommand({ type: "palette" })} aria-label="Command palette">
          <Icon d={ICONS.search} /> <kbd>{IS_MAC ? "⌘K" : "Ctrl K"}</kbd>
        </button>
        <button type="button" className={`mm-icon-btn${ctl.inspectorOpen ? " mm-on" : ""}`} onClick={() => onCommand({ type: "inspector" })} aria-label="Toggle inspector"><Icon d={ICONS.panel} /></button>
      </header>
      {proposals.length && ctl.showGhosts ? (
        <div className="mm-proposals">
          {proposals.map((p) => {
            const author = e.participants.get(p.author.participant_id);
            const n = p.items.filter((i) => i.status === "pending").length;
            if (!n) return null;
            return (
              <div className="mm-proposal" key={p.id} style={{ "--mm-agent": author?.color ?? "var(--primary)" }}>
                <Icon d={ICONS.ghost} />
                <span><strong>{author?.name ?? "An agent"}</strong> proposes {n} change{n > 1 ? "s" : ""}{p.summary ? `: ${p.summary}` : ""}</span>
                <button type="button" className="mm-btn mm-btn-xs mm-btn-primary" onClick={() => ctl.review(p.id, { accept_all: true })}>Accept all</button>
                <button type="button" className="mm-btn mm-btn-xs" onClick={() => ctl.review(p.id, { reject_all: true })}>Reject all</button>
              </div>
            );
          })}
        </div>
      ) : null}
      <div className="mm-body">
        <Canvas ctl={ctl} apiRef={apiRef} onCommand={onCommand} />
        {ctl.inspectorOpen ? <Inspector ctl={ctl} onClose={() => { ctl.inspectorOpen = false; ctl.bump(); }} /> : null}
      </div>
      {ctl.paletteOpen ? (
        <Palette ctl={ctl} actions={actions} onClose={closePalette} onPick={(id) => {
          ctl.select(id);
          apiRef.current?.reveal(id);
        }} />
      ) : null}
      {props.dev && /[?&](perf|seed)=/.test(location.search) ? <PerfOverlay ctl={ctl} /> : null}
    </div>
  );
}

function MapTitle(props: { ctl: MapController }) {
  const { ctl } = props;
  const map = ctl.engine.confirmed.map;
  const [editing, setEditing] = React.useState(false);
  if (!map) return <span className="mm-map-title mm-muted">Connecting…</span>;
  if (editing) {
    return (
      <input
        className="mm-input mm-map-title-input"
        defaultValue={map.title}
        maxLength={200}
        ref={(el: HTMLInputElement | null) => el?.focus()}
        onBlur={(e: FocusEvent) => {
          const t = (e.target as HTMLInputElement).value.trim();
          setEditing(false);
          if (t && t !== map.title) ctl.edit([{ type: "map.update", payload: { title: t } }]);
        }}
        onKeyDown={(e: KeyboardEvent) => {
          if (e.key === "Enter") (e.target as HTMLInputElement).blur();
          if (e.key === "Escape") setEditing(false);
        }}
        aria-label="Map title"
      />
    );
  }
  const title = ctl.view.map?.title ?? map.title;
  return <button type="button" className="mm-map-title" onClick={() => setEditing(true)} title="Rename map">{title}</button>;
}

function ConnChip(props: { state: string }) {
  const label = props.state === "ws" ? "Live" : props.state === "longpoll" ? "Live (fallback)" : props.state === "offline" ? "Offline" : "Connecting…";
  return <span className={`mm-conn mm-conn-${props.state}`} title={props.state === "longpoll" ? "WebSocket blocked: syncing through Kandev long-poll" : label}><i />{label}</span>;
}

/** Dev-only: frame times and layout cost, for the ?seed=1000 measurements. */
function PerfOverlay(props: { ctl: MapController }) {
  const ref = React.useRef(null as HTMLDivElement | null);
  React.useEffect(() => {
    let raf = 0;
    let last = performance.now();
    const frames: number[] = [];
    const tick = (t: number) => {
      frames.push(t - last);
      last = t;
      if (frames.length > 120) frames.shift();
      if (ref.current && frames.length % 10 === 0) {
        const avg = frames.reduce((a, b) => a + b, 0) / frames.length;
        const worst = Math.max(...frames);
        const nodes = props.ctl.display.layout.boxes.size;
        const dom = document.querySelectorAll(".mm-node").length;
        ref.current.textContent = `${(1000 / avg).toFixed(0)} fps · worst ${worst.toFixed(1)} ms · layout ${props.ctl.lastLayoutMs.toFixed(2)} ms · ${dom}/${nodes} cards`;
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, []);
  return <div ref={ref} className="mm-perf" data-testid="mm-perf" />;
}

