// Right-click menu on a node: agent flows plus a few editing shortcuts.
import type { MapNode } from "../contract";
import { h, React } from "../runtime";
import type { RunKind } from "./runForm";

export interface NodeMenuItem {
  id: string;
  label: string;
  disabled?: boolean;
  run(): void;
}

/** Agent entries for a node; execute only applies to action nodes. */
export function agentMenuItems(node: Pick<MapNode, "kind">, open: (kind: RunKind) => void): NodeMenuItem[] {
  return [
    { id: "run", label: "Run agent…", run: () => open("run") },
    { id: "swarm", label: "Swarm…", run: () => open("swarm") },
    { id: "explore", label: "Explore from here…", run: () => open("explore") },
    { id: "execute", label: "Execute…", disabled: node.kind !== "action", run: () => open("execute") },
    { id: "garden", label: "Garden the map…", run: () => open("garden") },
  ];
}

export function NodeMenu(props: { x: number; y: number; items: NodeMenuItem[][]; onClose: () => void }) {
  const ref = React.useRef(null as HTMLDivElement | null);
  const [pos, setPos] = React.useState({ x: props.x, y: props.y });
  React.useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    setPos({ x: Math.max(4, Math.min(props.x, innerWidth - r.width - 4)), y: Math.max(4, Math.min(props.y, innerHeight - r.height - 4)) });
    (el.querySelector("button:not([disabled])") as HTMLButtonElement | null)?.focus();
  }, [props.x, props.y]);
  React.useEffect(() => {
    const onDown = (e: PointerEvent) => {
      if (!ref.current?.contains(e.target as Node)) props.onClose();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        props.onClose();
      } else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        const btns = [...(ref.current?.querySelectorAll("button:not([disabled])") ?? [])] as HTMLButtonElement[];
        const i = btns.indexOf(document.activeElement as HTMLButtonElement);
        btns[(i + (e.key === "ArrowDown" ? 1 : -1) + btns.length) % btns.length]?.focus();
      }
    };
    document.addEventListener("pointerdown", onDown, true);
    document.addEventListener("keydown", onKey, true);
    return () => {
      document.removeEventListener("pointerdown", onDown, true);
      document.removeEventListener("keydown", onKey, true);
    };
  }, []);
  return (
    <div ref={ref} className="mm-menu" role="menu" style={{ left: pos.x, top: pos.y }} data-testid="mm-node-menu">
      {props.items.map((group, gi) => (
        <div key={gi} className="mm-menu-group">
          {group.map((it) => (
            <button
              key={it.id}
              type="button"
              role="menuitem"
              className="mm-menu-item"
              disabled={it.disabled}
              onClick={() => {
                props.onClose();
                it.run();
              }}
            >
              {it.label}
            </button>
          ))}
        </div>
      ))}
    </div>
  );
}
