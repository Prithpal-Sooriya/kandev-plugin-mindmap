// Shared root for both hosts: map list (create, rename, open) and the canvas.
import { ContractError, newId, type MapSummary } from "../contract";
import { seedDrafts } from "../dev/seed";
import type { HostAdapter } from "../host/adapter";
import { h, React } from "../runtime";
import { useAsync } from "./hooks";
import { Icon, ICONS, MindmapIcon } from "./icons";
import { MapView } from "./MapView";

declare const __DEV__: boolean;
const DEV = typeof __DEV__ !== "undefined" && __DEV__;

export function App(props: { host: HostAdapter }) {
  const { host } = props;
  const [mapId, setMapId] = React.useState(host.initialMapId());
  const [guest, setGuest] = React.useState(host.guestIdentity ? host.guestIdentity() : null);
  const needsGuest = !!host.guestIdentity && !guest;
  const [theme, setTheme] = React.useState(host.theme());
  React.useEffect(() => host.onThemeChange(setTheme), [host]);

  const open = (id: string | null) => {
    setMapId(id);
    host.setMapInUrl(id);
  };

  // Share links reach exactly one map: open it directly.
  const single = useAsync(async () => (!host.canListMaps && !mapId ? host.api.listMaps() : null), [host, mapId]);
  React.useEffect(() => {
    if (single.data?.length && !mapId) setMapId(single.data[0]!.id);
  }, [single.data]);

  let body: unknown;
  if (needsGuest) body = <GuestGate onDone={(g) => setGuest(g)} />;
  else if (mapId) body = <MapView key={mapId} host={host} mapId={mapId} guest={guest} dev={DEV} onBack={host.canListMaps ? () => open(null) : undefined} />;
  else if (host.canListMaps) body = <MapList host={host} onOpen={open} />;
  else body = <div className="mm-center mm-muted">{single.error ? "This link is no longer valid." : "Opening map…"}</div>;
  return <div className={`mm-root mm-theme-${theme}`} data-mm-host={host.kind}>{body}</div>;
}

function MapList(props: { host: HostAdapter; onOpen: (id: string) => void }) {
  const { host } = props;
  const maps = useAsync(() => host.api.listMaps(), [host]);
  const [title, setTitle] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [renaming, setRenaming] = React.useState(null as string | null);
  const seeded = React.useRef(false);

  const create = async (t: string, seed = 0) => {
    const ws = host.workspaceId();
    if (!t.trim()) return;
    setBusy(true);
    try {
      const m = await host.api.createMap({ workspace_id: ws ?? "default", title: t.trim() });
      if (seed) await seedMap(host, m.id, m.root_id, seed);
      props.onOpen(m.id);
    } catch (e) {
      host.toast(e instanceof ContractError ? e.message : "Could not create the map.", "error");
    } finally {
      setBusy(false);
    }
  };

  // Dev only: ?seed=N creates and opens an N-node map for perf measurements.
  React.useEffect(() => {
    if (!DEV || seeded.current) return;
    const n = Number(new URLSearchParams(location.search).get("seed"));
    if (n > 0) {
      seeded.current = true;
      void create(`Seeded map (${n} nodes)`, n);
    }
  }, []);

  const rename = async (m: MapSummary, t: string) => {
    setRenaming(null);
    if (!t.trim() || t.trim() === m.title) return;
    try {
      const pid = host.participant()?.id ?? "pt_host00000000";
      await host.endpoints.submit(m.id, {
        client_id: "maplist",
        ops: [{ id: newId("op"), map_id: m.id, rev: 0, type: "map.update", payload: { title: t.trim() }, actor: { participant_id: pid } }],
      });
      maps.reload();
    } catch {
      host.toast("Could not rename the map.", "error");
    }
  };

  return (
    <div className="mm-list" data-testid="mm-map-list">
      <header className="mm-list-head">
        <MindmapIcon size={20} />
        <h2>Mind maps</h2>
      </header>
      <form className="mm-create" onSubmit={(e: Event) => { e.preventDefault(); void create(title); setTitle(""); }}>
        <input className="mm-input" placeholder="Central topic of a new map…" value={title} maxLength={200} onInput={(e: Event) => setTitle((e.target as HTMLInputElement).value)} aria-label="New map title" />
        <button type="submit" className="mm-btn mm-btn-primary" disabled={busy || !title.trim()}><Icon d={ICONS.plus} /> New map</button>
      </form>
      {maps.loading && !maps.data ? <p className="mm-muted">Loading…</p> : null}
      {maps.error ? <p className="mm-warn">Could not load maps: {maps.error} <button type="button" className="mm-btn mm-btn-xs" onClick={maps.reload}>Retry</button></p> : null}
      {maps.data && !maps.data.length ? <p className="mm-muted">No maps yet. Name a central topic above to start one.</p> : null}
      <ul className="mm-map-cards">
        {(maps.data ?? []).slice().sort((a, b) => b.updated_at.localeCompare(a.updated_at)).map((m) => (
          <li key={m.id} className="mm-map-card">
            {renaming === m.id ? (
              <input
                className="mm-input"
                defaultValue={m.title}
                ref={(el: HTMLInputElement | null) => el?.focus()}
                onBlur={(e: FocusEvent) => void rename(m, (e.target as HTMLInputElement).value)}
                onKeyDown={(e: KeyboardEvent) => {
                  if (e.key === "Enter") (e.target as HTMLInputElement).blur();
                  if (e.key === "Escape") setRenaming(null);
                }}
                aria-label="Rename map"
              />
            ) : (
              <button type="button" className="mm-map-open" onClick={() => props.onOpen(m.id)}>
                <strong>{m.title}</strong>
                <span className="mm-muted">{m.nodes} nodes{m.active_runs ? ` · ${m.active_runs} agents running` : ""}{m.shared ? " · shared" : ""}</span>
              </button>
            )}
            <button type="button" className="mm-icon-btn" onClick={() => setRenaming(m.id)} aria-label={`Rename ${m.title}`}><Icon d={ICONS.edit} /></button>
          </li>
        ))}
      </ul>
    </div>
  );
}

const GUEST_COLORS = ["#ef4444", "#f59e0b", "#22c55e", "#06b6d4", "#3b82f6", "#a855f7", "#ec4899"];

function GuestGate(props: { onDone: (g: { name: string; color: string }) => void }) {
  const [name, setName] = React.useState("");
  const [color, setColor] = React.useState(GUEST_COLORS[Math.floor(Math.random() * GUEST_COLORS.length)]!);
  return (
    <form className="mm-guest" onSubmit={(e: Event) => {
      e.preventDefault();
      if (!name.trim()) return;
      const g = { name: name.trim().slice(0, 40), color };
      try {
        localStorage.setItem("mm-guest", JSON.stringify(g));
      } catch {
        // private mode: identity lasts for this tab only
      }
      props.onDone(g);
    }}>
      <MindmapIcon size={28} />
      <h2>Join the mind map</h2>
      <p className="mm-muted">Pick a name and a colour so others can see your cursor and edits.</p>
      <input className="mm-input" placeholder="Your name" value={name} maxLength={40} onInput={(e: Event) => setName((e.target as HTMLInputElement).value)} aria-label="Your name" autoFocus />
      <div className="mm-swatches">
        {GUEST_COLORS.map((c) => (
          <button type="button" key={c} className={`mm-swatch${c === color ? " mm-on" : ""}`} style={{ background: c }} onClick={() => setColor(c)} aria-label={`Colour ${c}`} />
        ))}
      </div>
      <button type="submit" className="mm-btn mm-btn-primary" disabled={!name.trim()}>Join</button>
    </form>
  );
}

/** Dev seeding: N creates through the real submit path, in contract-sized batches. */
async function seedMap(host: HostAdapter, mapId: string, rootId: string, n: number): Promise<void> {
  const drafts = seedDrafts(rootId, n, 1, newId("op").slice(3, 9));
  const pid = host.participant()?.id ?? "pt_host00000000";
  for (let i = 0; i < drafts.length; i += 200) {
    const batch = newId("bt");
    await host.endpoints.submit(mapId, {
      client_id: "seed",
      ops: drafts.slice(i, i + 200).map((d) => ({ id: newId("op"), map_id: mapId, rev: 0, ...d, actor: { participant_id: pid }, batch_id: batch }) as never),
    });
  }
}

