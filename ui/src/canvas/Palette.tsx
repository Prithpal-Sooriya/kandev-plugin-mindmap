// ⌘K command palette. Search works now (titles, tags, bodies, kinds); agent
// actions are Phase 2 stubs that say so.
import type { MapNode } from "../contract";
import { h, React } from "../runtime";
import type { MapController } from "./controller";
import { KIND_LABEL, KindIcon } from "./icons";

export interface PaletteAction {
  id: string;
  label: string;
  hint?: string;
  run(): void;
}

/** Ranks nodes for a query: title prefix > title word > title substring > tag/kind > body. */
export function searchNodes(nodes: Iterable<MapNode>, query: string, limit = 30): MapNode[] {
  const q = query.trim().toLowerCase();
  if (!q) return [];
  const scored: { n: MapNode; s: number }[] = [];
  for (const n of nodes) {
    if (n.deleted || n.id.startsWith("ghost:")) continue;
    const t = n.title.toLowerCase();
    let s = 0;
    if (t.startsWith(q)) s = 100;
    else if (t.includes(` ${q}`)) s = 80;
    else if (t.includes(q)) s = 60;
    else if (n.tags?.some((tag) => tag.includes(q.replace(/^#/, "")))) s = 50;
    else if (n.kind === q || KIND_LABEL[n.kind].toLowerCase() === q) s = 40;
    else if (n.body_md?.toLowerCase().includes(q)) s = 20;
    if (s) scored.push({ n, s: s - Math.min(t.length, 60) / 100 });
  }
  return scored.sort((a, b) => b.s - a.s).slice(0, limit).map((x) => x.n);
}

export function Palette(props: { ctl: MapController; actions: PaletteAction[]; onPick: (id: string) => void; onClose: () => void }) {
  const [q, setQ] = React.useState(() => {
    const seed = props.ctl.paletteSeed;
    props.ctl.paletteSeed = "";
    return seed;
  });
  const [active, setActive] = React.useState(0);
  const inputRef = React.useRef(null as HTMLInputElement | null);
  React.useLayoutEffect(() => {
    const el = inputRef.current;
    el?.focus();
    el?.setSelectionRange(el.value.length, el.value.length);
  }, []);

  const ql = q.trim().toLowerCase();
  const actions = props.actions.filter((a) => !ql || a.label.toLowerCase().includes(ql.replace(/^>/, "")));
  const results = ql.startsWith(">") ? [] : searchNodes(props.ctl.view.nodes.values(), q);
  const items: { key: string; el: unknown; run: () => void }[] = [
    ...results.map((n) => ({
      key: n.id,
      run: () => props.onPick(n.id),
      el: (
        <span className="mm-pal-node">
          <KindIcon kind={n.kind} />
          <span className="mm-ellipsis">{n.title}</span>
          {n.tags?.length ? <span className="mm-muted">#{n.tags[0]}</span> : null}
        </span>
      ),
    })),
    ...actions.map((a) => ({
      key: a.id,
      run: a.run,
      el: (
        <span className="mm-pal-action">
          <span>{a.label}</span>
          {a.hint ? <span className="mm-muted">{a.hint}</span> : null}
        </span>
      ),
    })),
  ];
  const sel = Math.min(active, Math.max(0, items.length - 1));

  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Escape") {
      e.preventDefault();
      props.onClose();
    } else if (e.key === "ArrowDown") {
      e.preventDefault();
      setActive((sel + 1) % Math.max(1, items.length));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActive((sel - 1 + items.length) % Math.max(1, items.length));
    } else if (e.key === "Enter") {
      e.preventDefault();
      const it = items[sel];
      props.onClose();
      it?.run();
    }
  };

  return (
    <div className="mm-palette-backdrop" onPointerDown={(e: PointerEvent) => e.target === e.currentTarget && props.onClose()}>
      <div className="mm-palette" role="dialog" aria-label="Command palette" data-testid="mm-palette">
        <input
          ref={inputRef}
          className="mm-palette-input"
          placeholder="Search nodes, or type > for commands"
          value={q}
          onInput={(e: Event) => {
            setQ((e.target as HTMLInputElement).value);
            setActive(0);
          }}
          onKeyDown={onKeyDown}
          aria-label="Search"
        />
        <ul className="mm-palette-list mm-scroll" role="listbox">
          {items.length ? items.map((it, i) => (
            <li
              key={it.key}
              role="option"
              aria-selected={i === sel}
              className={i === sel ? "mm-active" : ""}
              onPointerEnter={() => setActive(i)}
              onPointerDown={(e: PointerEvent) => {
                e.preventDefault();
                props.onClose();
                it.run();
              }}
            >
              {it.el}
            </li>
          )) : <li className="mm-muted">No matches</li>}
        </ul>
      </div>
    </div>
  );
}

