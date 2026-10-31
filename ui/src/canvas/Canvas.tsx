// The map canvas: HTML cards over an SVG edge layer inside one transformed
// "world" element. Pan/zoom only rewrites that element's CSS transform (no
// React render); React re-renders only when the culled region, the LOD or the
// data changes, and unchanged cards/edges are reused by element identity.
import { buildParentIndex, type Participant } from "../contract";
import { dropTarget, type DropTarget } from "../layout/drop";
import {
  arrowPath, contains, cull, expand, fitCamera, linkPath, navigate, revealBox, screenToWorld, treeEdgePath,
  viewportWorld, zoomAt, type Camera,
} from "../layout/geometry";
import type { Box } from "../layout/tree";
import { h, React } from "../runtime";
import { ringFor } from "./agent";
import type { MapController } from "./controller";
import { keyToCommand, type Command } from "./keys";
import { NodeCard, propsEqual, type CardProps } from "./NodeCard";
import { Minimap } from "./minimap";

const LOD_ZOOM = 0.45;
const MOUNT_PER_FRAME = 150;
const IS_MAC = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

interface DragState {
  id: string;
  x: number; // world point under the pointer
  y: number;
  dx: number; // offset of the pointer inside the card
  dy: number;
  target: DropTarget | null;
}

export interface CanvasApi {
  fit(): void;
  zoom(factor: number): void;
  reveal(id: string): void;
  focus(): void;
  command(c: Command): void;
}

export function Canvas(props: { ctl: MapController; apiRef: { current: CanvasApi | null }; onCommand: (c: Command) => void }) {
  const { ctl } = props;
  const viewportRef = React.useRef(null as HTMLDivElement | null);
  const worldRef = React.useRef(null as HTMLDivElement | null);
  const cam = React.useRef({ x: 0, y: 0, k: 1 } as Camera);
  const size = React.useRef({ w: 800, h: 600 });
  const [region, setRegion] = React.useState(null as { x: number; y: number; w: number; h: number } | null);
  const [lod, setLod] = React.useState(false);
  const [drag, setDrag] = React.useState(null as DragState | null);
  const [, setMountTick] = React.useState(0);
  const [thumbs, setThumbs] = React.useState(new Map<string, string>());
  const cardCache = React.useRef(new Map<string, { props: CardProps; el: unknown }>());
  const edgeCache = React.useRef(new Map<string, { p: Box; c: Box; ghost: boolean; el: unknown }>());
  const fitted = React.useRef(false);
  const raf = React.useRef(0);
  const lastK = React.useRef(0);
  const rasterK = React.useRef(1);
  const settle = React.useRef(null as ReturnType<typeof setTimeout> | null);
  const pointer = React.useRef(null as null | { mode: "pan" | "node"; id?: string; sx: number; sy: number; cx: number; cy: number; moved: boolean; pid: number });

  const display = ctl.display;
  const { boxes, children, bounds } = display.layout;
  const engine = ctl.engine;
  const me = engine.participant?.id;

  // --- camera ----------------------------------------------------------------------------

  const applyCamera = React.useCallback(() => {
    raf.current = 0;
    const w = worldRef.current;
    if (!w) return;
    const c = cam.current;
    w.style.transform = `translate(${c.x}px,${c.y}px) scale(${c.k})`;
    if (lastK.current !== c.k) {
      lastK.current = c.k;
      w.style.setProperty("--mm-k", String(c.k));
    }
    const vp = viewportWorld(c, size.current.w, size.current.h);
    // Re-cull when the view leaves the rendered region, or after zooming in far
    // enough that most rendered cards are offscreen.
    setRegion((r: typeof region) => (r && contains(r, vp) && r.w < vp.w * 3.5 ? r : expand(vp, 0.6)));
    setLod((l: boolean) => (c.k < LOD_ZOOM) !== l ? c.k < LOD_ZOOM : l);
    ctl.publishPresence({ viewport: { x: vp.x, y: vp.y, w: vp.w, h: vp.h } });
    const mmVp = document.getElementById("mm-minimap-vp");
    if (mmVp) {
      mmVp.setAttribute("x", String(vp.x));
      mmVp.setAttribute("y", String(vp.y));
      mmVp.setAttribute("width", String(vp.w));
      mmVp.setAttribute("height", String(vp.h));
    }
  }, [ctl]);

  const schedule = React.useCallback(() => {
    if (!raf.current) raf.current = requestAnimationFrame(applyCamera);
    // Once a zoom settles, re-raster the world layer at the new scale (crisp text when
    // zooming in, cheap tiles when zooming out). Pans keep the existing raster.
    if (settle.current) clearTimeout(settle.current);
    settle.current = setTimeout(() => {
      const w = worldRef.current;
      if (!w || rasterK.current === cam.current.k) return;
      rasterK.current = cam.current.k;
      w.classList.add("mm-reraster");
      requestAnimationFrame(() => w.classList.remove("mm-reraster"));
    }, 200);
  }, [applyCamera]);

  const setCam = React.useCallback((c: Camera) => {
    cam.current = c;
    schedule();
  }, [schedule]);

  const fit = React.useCallback(() => {
    const b = ctl.display.layout.bounds;
    setCam(fitCamera(b, size.current.w, size.current.h));
  }, [ctl, setCam]);

  const reveal = React.useCallback((id: string) => {
    const b = ctl.display.layout.boxes.get(id);
    if (b) setCam(revealBox(cam.current, b, size.current.w, size.current.h));
  }, [ctl, setCam]);

  // Measure the viewport; fit once the first layout exists.
  React.useLayoutEffect(() => {
    const el = viewportRef.current;
    if (!el) return;
    const measure = () => {
      const r = el.getBoundingClientRect();
      size.current = { w: r.width || 800, h: r.height || 600 };
      if (!fitted.current && ctl.display.layout.boxes.size) {
        fitted.current = true;
        cam.current = fitCamera(ctl.display.layout.bounds, size.current.w, size.current.h);
      }
      applyCamera();
    };
    measure();
    const ro = typeof ResizeObserver !== "undefined" ? new ResizeObserver(measure) : null;
    ro?.observe(el);
    return () => ro?.disconnect();
  }, [ctl, applyCamera, boxes.size > 0]);

  // --- wheel, trackpad and pinch ------------------------------------------------------------

  React.useEffect(() => {
    const el = viewportRef.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      if ((e.target as HTMLElement).closest(".mm-editor, .mm-scroll")) return;
      e.preventDefault();
      const r = el.getBoundingClientRect();
      const sx = e.clientX - r.left, sy = e.clientY - r.top;
      const lineScale = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? size.current.h : 1;
      const dx = e.deltaX * lineScale, dy = e.deltaY * lineScale;
      // Pinch on a trackpad arrives as ctrl+wheel. A mouse wheel has no X and coarse integer steps.
      const mouseWheel = e.deltaMode === 1 || (e.deltaX === 0 && Number.isInteger(e.deltaY) && Math.abs(e.deltaY) >= 50);
      if (e.ctrlKey || e.metaKey) setCam(zoomAt(cam.current, sx, sy, Math.exp(-dy * 0.01)));
      else if (mouseWheel && !e.shiftKey) setCam(zoomAt(cam.current, sx, sy, Math.exp(-dy * 0.0015)));
      else setCam({ ...cam.current, x: cam.current.x - (e.shiftKey && !dx ? dy : dx), y: cam.current.y - (e.shiftKey && !dx ? 0 : dy) });
    };
    // Safari pinch.
    let gestureK = 1;
    const onGestureStart = (e: Event) => {
      e.preventDefault();
      gestureK = 1;
    };
    const onGestureChange = (e: Event) => {
      e.preventDefault();
      const ge = e as Event & { scale: number; clientX: number; clientY: number };
      const r = el.getBoundingClientRect();
      setCam(zoomAt(cam.current, ge.clientX - r.left, ge.clientY - r.top, ge.scale / gestureK));
      gestureK = ge.scale;
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    el.addEventListener("gesturestart", onGestureStart);
    el.addEventListener("gesturechange", onGestureChange);
    return () => {
      el.removeEventListener("wheel", onWheel);
      el.removeEventListener("gesturestart", onGestureStart);
      el.removeEventListener("gesturechange", onGestureChange);
    };
  }, [setCam]);

  // --- pointer: pan, select, drag-to-reparent, presence cursor ----------------------------------

  const worldPoint = (e: { clientX: number; clientY: number }) => {
    const r = viewportRef.current!.getBoundingClientRect();
    return screenToWorld(cam.current, e.clientX - r.left, e.clientY - r.top);
  };

  const onPointerDown = (e: PointerEvent) => {
    if (e.button !== 0 && e.button !== 1) return;
    const t = e.target as HTMLElement;
    if (t.closest(".mm-editor, .mm-node-toolbar")) return;
    const card = t.closest(".mm-node") as HTMLElement | null;
    const id = card?.dataset.id;
    viewportRef.current?.focus({ preventScroll: true });
    try {
      viewportRef.current?.setPointerCapture?.(e.pointerId);
    } catch {
      // synthetic or already-released pointer: dragging still works without capture
    }
    pointer.current = {
      mode: id && e.button === 0 ? "node" : "pan", id, sx: e.clientX, sy: e.clientY, cx: cam.current.x, cy: cam.current.y,
      moved: false, pid: e.pointerId,
    };
    if (ctl.editing && id !== ctl.editing.id) ctl.commitEdit();
  };

  const onPointerMove = (e: PointerEvent) => {
    const wp = worldPoint(e);
    ctl.publishPresence({ cursor: { x: Math.round(wp.x), y: Math.round(wp.y) } });
    const p = pointer.current;
    if (!p || p.pid !== e.pointerId) return;
    const dist = Math.hypot(e.clientX - p.sx, e.clientY - p.sy);
    if (!p.moved && dist < 4) return;
    p.moved = true;
    if (ctl.followingRun) { ctl.followingRun = null; ctl.bump(); }
    if (p.mode === "pan") {
      viewportRef.current?.classList.add("mm-panning");
      setCam({ ...cam.current, x: p.cx + (e.clientX - p.sx), y: p.cy + (e.clientY - p.sy) });
      return;
    }
    const id = p.id!;
    if (id === display.rootId || id.startsWith("ghost:")) return;
    const b = boxes.get(id);
    if (!b) return;
    const start = screenToWorld(cam.current, p.sx - viewportRef.current!.getBoundingClientRect().left, p.sy - viewportRef.current!.getBoundingClientRect().top);
    const dx = drag?.dx ?? start.x - b.x, dy = drag?.dy ?? start.y - b.y;
    const parents = buildParentIndex(ctl.view.nodes.values());
    const target = dropTarget(boxes, (pid) => ctl.siblingsOf(pid), parents, id, wp.x, wp.y);
    setDrag({ id, x: wp.x, y: wp.y, dx, dy, target });
  };

  const onPointerUp = (e: PointerEvent) => {
    const p = pointer.current;
    pointer.current = null;
    viewportRef.current?.classList.remove("mm-panning");
    if (!p || p.pid !== e.pointerId) return;
    if (p.mode === "node" && p.id) {
      if (drag && drag.target) ctl.move(drag.id, drag.target.parentId, drag.target.order);
      else if (!p.moved) ctl.select(p.id);
      setDrag(null);
      return;
    }
    if (p.mode === "pan" && !p.moved) {
      if (ctl.editing) ctl.commitEdit();
      ctl.select(null);
    }
  };

  const onDoubleClick = (e: MouseEvent) => {
    const id = ((e.target as HTMLElement).closest(".mm-node") as HTMLElement | null)?.dataset.id;
    if (id && !id.startsWith("ghost:")) {
      ctl.select(id);
      ctl.startEdit(id);
    } else if (!id) fit();
  };

  // --- keyboard ---------------------------------------------------------------------------

  const run = React.useCallback((c: Command) => {
    const sel = ctl.selection;
    switch (c.type) {
      case "addChild": {
        const id = ctl.addChild();
        if (id) reveal(id);
        return;
      }
      case "addSibling": {
        const id = ctl.addSibling();
        if (id) reveal(id);
        return;
      }
      case "edit":
        if (sel && !sel.startsWith("ghost:")) ctl.startEdit(sel, c.initial);
        return;
      case "nav": {
        if (!sel) return;
        const next = navigate(ctl.display.layout.boxes, ctl.display.layout.children, sel, c.dir);
        if (next) {
          ctl.select(next);
          reveal(next);
        }
        return;
      }
      case "selectRoot":
        ctl.select(ctl.rootId);
        reveal(ctl.rootId);
        return;
      case "toggleCollapse":
        return ctl.toggleCollapse();
      case "retire":
        return ctl.retire();
      case "undo":
        return ctl.undo();
      case "redo":
        return ctl.redo();
      case "fit":
        return fit();
      case "zoomIn":
        return setCam(zoomAt(cam.current, size.current.w / 2, size.current.h / 2, 1.25));
      case "zoomOut":
        return setCam(zoomAt(cam.current, size.current.w / 2, size.current.h / 2, 0.8));
      case "deselect":
        return ctl.select(null);
      default:
        return props.onCommand(c);
    }
  }, [ctl, reveal, fit, setCam, props.onCommand]);

  props.apiRef.current = {
    fit, reveal, zoom: (f) => setCam(zoomAt(cam.current, size.current.w / 2, size.current.h / 2, f)),
    focus: () => viewportRef.current?.focus({ preventScroll: true }), command: run,
  };

  const onKeyDown = (e: KeyboardEvent) => {
    if ((e.target as HTMLElement).closest(".mm-editor")) return;
    if (ctl.paletteOpen) {
      // The palette renders on the next frame: route early keystrokes into its query.
      if (e.key.length === 1 && !(e.metaKey || e.ctrlKey || e.altKey)) {
        ctl.paletteSeed += e.key;
        e.preventDefault();
      }
      return;
    }
    if (ctl.editing) {
      // The editor has not mounted yet (it renders on the next frame): keep the keystrokes.
      const c = keyToCommand(e, { editing: true, selected: true, mac: IS_MAC });
      if (c?.type === "commitEdit") ctl.commitEdit();
      else if (c?.type === "cancelEdit") ctl.cancelEdit();
      else if (!(e.metaKey || e.ctrlKey || e.altKey) && ctl.typeAhead(e.key)) {
        /* consumed */
      } else return;
      e.preventDefault();
      e.stopPropagation();
      if (c?.type === "commitEdit" && e.key === "Tab") run({ type: "addChild" });
      return;
    }
    const c = keyToCommand(e, { editing: false, selected: !!ctl.selection, mac: IS_MAC });
    if (!c) return;
    e.preventDefault();
    e.stopPropagation();
    run(c);
  };

  // --- thumbnails (asset webhook in Kandev, guest asset route standalone) ---------------------------

  const wantThumb = (assetId: string) => {
    if (thumbs.has(assetId)) return;
    thumbs.set(assetId, "");
    ctl.host.assetUrl(ctl.mapId, assetId).then(
      (url) => setThumbs((m: Map<string, string>) => new Map(m).set(assetId, url)),
      () => {},
    );
  };

  // --- render ------------------------------------------------------------------------------

  const presenceById = new Map<string, Participant>();
  for (const p of engine.participants.values()) presenceById.set(p.id, p);
  const remoteSel = new Map<string, string>();
  const editingBy = new Map<string, { name: string; color: string }>();
  const now = Date.now();
  for (const pr of engine.presence.values()) {
    if (pr.participant_id === me) continue;
    const who = presenceById.get(pr.participant_id);
    const color = who?.color ?? "#888";
    for (const id of pr.selection ?? []) if (!remoteSel.has(id)) remoteSel.set(id, color);
    if (pr.editing && Date.parse(pr.editing.expires_at) > now) editingBy.set(pr.editing.node_id, { name: who?.name ?? "Someone", color });
  }

  const pinned = [ctl.selection, ctl.editing?.id].filter(Boolean) as string[];
  const visible = region ? cull(boxes.values(), region, pinned) : new Set<string>();
  const cards: unknown[] = [];
  const nextCards = new Map<string, { props: CardProps; el: unknown }>();
  // Mount at most MOUNT_PER_FRAME new cards per render so a big zoom-out fills
  // in over a few frames instead of one long one.
  let mounts = 0;
  let deferred = false;
  for (const id of visible) {
    if (!cardCache.current.has(id) && !pinned.includes(id)) {
      if (mounts >= MOUNT_PER_FRAME) {
        deferred = true;
        continue;
      }
      mounts++;
    }
    const node = display.nodes.get(id);
    const box = boxes.get(id);
    if (!node || !box) continue;
    const runs = display.runsByNode.get(id);
    const ring = ringFor(node, runs);
    const agentP = (runs ?? []).filter((r) => r.status === "running" || r.status === "waiting").map((r) => presenceById.get(r.participant_id)).filter(Boolean) as Participant[];
    const tm = engine.trail.get(id);
    const asset = node.assets?.find((a) => a.kind === "image");
    const contested = display.links.some((l) => l.to === id && (l.label?.toLowerCase().includes("contest") || l.label?.toLowerCase().includes("counter")));
    if (asset) wantThumb(asset.id);
    const cp: CardProps = {
      node, box, isRoot: id === display.rootId, selected: ctl.selection === id, remoteSelect: remoteSel.get(id) ?? null,
      editingBy: editingBy.get(id) ?? null,
      ring: ring ? { ...ring, color: ring.run ? presenceById.get(ring.run.participant_id)?.color : undefined, count: display.pendingByNode.get(id) } : null,
      agents: agentP, trail: tm ? { at: tm.at, color: presenceById.get(tm.participantId)?.color ?? "var(--primary)" } : null,
      unseen: engine.unseen.has(id), ghosts: display.ghosts.get(id), dragging: drag?.id === id,
      hasThumb: !!asset, thumbUrl: asset ? thumbs.get(asset.id) || undefined : undefined, contested,
      hideTitle: ctl.editing?.id === id,
    };
    const cached = cardCache.current.get(id);
    const entry = cached && propsEqual(cached.props, cp) ? cached : { props: cp, el: h(NodeCard, { key: id, ...cp }) };
    nextCards.set(id, entry);
    cards.push(entry.el);
  }
  cardCache.current = nextCards;
  React.useEffect(() => {
    if (!deferred) return;
    const r = requestAnimationFrame(() => setMountTick((t: number) => t + 1));
    return () => cancelAnimationFrame(r);
  });

  // Tree edges for every visible child (and children of visible parents).
  const edges: unknown[] = [];
  const nextEdges = new Map<string, { p: Box; c: Box; ghost: boolean; el: unknown }>();
  for (const b of boxes.values()) {
    if (!b.parentId) continue;
    if (!visible.has(b.id) && !visible.has(b.parentId)) continue;
    const p = boxes.get(b.parentId);
    if (!p) continue;
    const ghost = !!display.ghosts.get(b.id)?.some((g) => g.kind === "create" || g.kind === "move");
    const cached = edgeCache.current.get(b.id);
    const entry = cached && cached.p === p && cached.c === b && cached.ghost === ghost
      ? cached
      : { p, c: b, ghost, el: h("path", { key: b.id, d: treeEdgePath(p, b), className: `mm-edge mm-edge-d${Math.min(b.depth, 3)}${ghost ? " mm-edge-ghost" : ""}` }) };
    nextEdges.set(b.id, entry);
    edges.push(entry.el);
  }
  edgeCache.current = nextEdges;

  const links = [...display.links.map((l) => ({ l, ghost: false })), ...display.ghostLinks.map((g) => ({ l: g.link, ghost: true }))]
    .map(({ l, ghost }) => {
      const a = boxes.get(l.from), b = boxes.get(l.to);
      if (!a || !b || (!visible.has(a.id) && !visible.has(b.id))) return null;
      const lp = linkPath(a, b);
      return (
        <g key={l.id} className={`mm-link${ghost ? " mm-link-ghost" : ""}`}>
          <path d={lp.d} markerEnd="url(#mm-arrow)" />
          {l.label && !lod ? <text x={lp.lx} y={lp.ly} textAnchor="middle" dy="-4">{l.label}</text> : null}
        </g>
      );
    });

  const halos = computeHalos(ctl).map((h, i) => <rect key={`halo-${h.id}`} x={h.box.x} y={h.box.y} width={h.box.w} height={h.box.h} rx="16" fill={h.color} opacity="0.1" />);
  const arrows = display.arrows.map((a, i) => {
    const from = boxes.get(a.from), to = boxes.get(a.to);
    if (!from || !to) return null;
    return <path key={`a${i}`} className="mm-ghost-arrow" d={arrowPath(from, to)} markerEnd="url(#mm-arrow-ghost)" />;
  });

  // Remote cursors (world coordinates, counter-scaled so they keep their size).
  const cursors = [...engine.presence.values()]
    .filter((p) => p.participant_id !== me && p.cursor && now - Date.parse(p.updated_at) < 30_000)
    .map((p) => {
      const who = presenceById.get(p.participant_id);
      return (
        <div key={p.participant_id} className="mm-cursor" style={{ transform: `translate(${p.cursor!.x}px,${p.cursor!.y}px) scale(calc(1 / var(--mm-k, 1)))`, "--mm-agent": who?.color ?? "#888" }}>
          <svg width="16" height="16" viewBox="0 0 16 16"><path d="M1 1l5 14l2-6l6-2z" /></svg>
          <span>{who?.name ?? "Guest"}</span>
        </div>
      );
    });

  const dragBox = drag ? boxes.get(drag.id) : null;
  const selBox = ctl.selection ? boxes.get(ctl.selection) : null;
  const selGhosts = ctl.selection ? display.ghosts.get(ctl.selection) : undefined;
  React.useEffect(() => {;
    if (!ctl.followingRun) return;;
    const run = ctl.engine.runs.get(ctl.followingRun);;
    if (!run) return;;
    let latestNode = run.node_id;;
    let latestTime = 0;;
    for (const [id, tm] of ctl.engine.trail.entries()) {;
      if (tm.participantId === run.participant_id && tm.at > latestTime) {;
        latestTime = tm.at;;
        latestNode = id;;
      }
    };
    if (latestTime > (ctl as any).lastTrailTime) {;
      (ctl as any).lastTrailTime = latestTime;;
      props.apiRef.current?.reveal(latestNode);
    };
  });
  React.useEffect(() => {;
    if (!region || engine.unseen.size === 0) return;;
    const t = setTimeout(() => {
      let changed = false;
      for (const id of engine.unseen) {
        const b = boxes.get(id);
        if (b && b.x >= region.x && b.y >= region.y && b.x + b.w <= region.x + region.w && b.y + b.h <= region.y + region.h) {
          engine.markSeen(id);
          changed = true;
        };
      };
      if (changed) ctl.bump();
    }, 1000);
    return () => clearTimeout(t);  }, [region, engine.unseen.size]);

  return (
    <div
      ref={viewportRef}
      className={`mm-viewport${lod ? " mm-lod-on" : ""}${drag ? " mm-dragging-on" : ""}`}
      tabIndex={0}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
      onDoubleClick={onDoubleClick}
      onKeyDown={onKeyDown}
      data-testid="mm-canvas"
      aria-label="Mind map canvas"
    >
      <div ref={worldRef} className="mm-world">
        <svg
          className="mm-edges"
          style={{ left: bounds.x - 400, top: bounds.y - 400 }}
          width={bounds.w + 800}
          height={bounds.h + 800}
          viewBox={`${bounds.x - 400} ${bounds.y - 400} ${bounds.w + 800} ${bounds.h + 800}`}
          aria-hidden="true"
        >
          <defs>
            <marker id="mm-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0 0L10 5L0 10z" className="mm-arrowhead" /></marker>
            <marker id="mm-arrow-ghost" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="8" markerHeight="8" orient="auto-start-reverse"><path d="M0 0L10 5L0 10z" className="mm-arrowhead-ghost" /></marker>
          </defs>
          {halos}
          {edges}
          {links}
          {arrows}
          {drag?.target ? (
            drag.target.mode === "child"
              ? <rect className="mm-drop-target" x={drag.target.indicator.x} y={drag.target.indicator.y} width={drag.target.indicator.w} height={drag.target.indicator.h} rx="10" />
              : <rect className="mm-drop-line" x={drag.target.indicator.x} y={drag.target.indicator.y} width={drag.target.indicator.w} height="3" rx="1.5" />
          ) : null}
        </svg>
        {cards}
        {drag && dragBox ? (
          <div className="mm-drag-ghost" style={{ transform: `translate(${drag.x - drag.dx}px,${drag.y - drag.dy}px)`, width: dragBox.w, height: dragBox.h }}>
            {display.nodes.get(drag.id)?.title}
          </div>
        ) : null}
        {ctl.editing && boxes.get(ctl.editing.id) ? <InlineEditor ctl={ctl} box={boxes.get(ctl.editing.id)!} isRoot={ctl.editing.id === display.rootId} onDone={(next) => {
          viewportRef.current?.focus({ preventScroll: true });
          if (next) run(next);
        }} /> : null}
        {selBox && selGhosts?.length && !drag ? (
          <div className="mm-node-toolbar" style={{ transform: `translate(${selBox.x}px,${selBox.y + selBox.h + 6}px) scale(calc(1 / var(--mm-k, 1)))` }}>
            <span>{selGhosts.map((g) => g.kind).join(", ")}</span>
            <button type="button" className="mm-btn mm-btn-xs mm-btn-primary" onClick={() => ctl.reviewNode(ctl.selection!, true)}>Accept</button>
            <button type="button" className="mm-btn mm-btn-xs" onClick={() => ctl.reviewNode(ctl.selection!, false)}>Reject</button>
          </div>
        ) : null}
        {cursors}
      </div>
      {boxes.size === 0 ? <div className="mm-empty">Loading map…</div> : null}
      <div className="mm-hint" aria-hidden="true">
        {IS_MAC ? "⌘K" : "Ctrl K"} commands · Tab child · Enter sibling · Space collapse · drag to pan
      </div>
      {region && <OffscreenMarkers ctl={ctl} region={region} cam={cam.current} size={size.current} onFly={(id) => props.apiRef.current?.reveal(id)} />}
      {region && <Minimap ctl={ctl} region={region} onNavigate={(x, y) => setCam({ ...cam.current, x: size.current.w / 2 - x * cam.current.k, y: size.current.h / 2 - y * cam.current.k })} />}
      <span className="mm-sr">{visible.size} of {boxes.size} nodes rendered</span>
    </div>
  );
}

function InlineEditor(props: { ctl: MapController; box: Box; isRoot: boolean; onDone: (next?: Command) => void }) {
  const { ctl, box } = props;
  const ref = React.useRef(null as HTMLTextAreaElement | null);
  const editing = ctl.editing!;
  React.useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.focus({ preventScroll: true });
    if (editing.touched) el.setSelectionRange(el.value.length, el.value.length);
    else el.select();
  }, [editing.id]);
  const onKeyDown = (e: KeyboardEvent) => {
    const c = keyToCommand(e, { editing: true, selected: true, mac: IS_MAC });
    if (!c) return;
    if (c.type === "palette") return;
    e.preventDefault();
    e.stopPropagation();
    if (c.type === "commitEdit") {
      ctl.commitEdit();
      props.onDone(e.key === "Tab" ? { type: "addChild" } : undefined);
    } else if (c.type === "cancelEdit") {
      ctl.cancelEdit();
      props.onDone();
    }
  };
  return (
    <textarea
      key={editing.id}
      ref={ref}
      className={`mm-editor${props.isRoot ? " mm-editor-root" : ""}`}
      style={{ transform: `translate(${box.x}px,${box.y}px)`, width: box.w, height: box.h }}
      defaultValue={ctl.draft?.title ?? ctl.node(editing.id)?.title ?? ""}
      onInput={(e: Event) => ctl.setDraft((e.target as HTMLTextAreaElement).value)}
      onKeyDown={onKeyDown}
      onBlur={() => {
        if (ctl.editing?.id === editing.id) ctl.commitEdit();
      }}
      spellCheck={false}
      aria-label="Node title"
      data-testid="mm-editor"
    />
  );
}


function computeHalos(ctl: MapController) {
  const halos: { id: string; color: string; box: { x: number; y: number; w: number; h: number } }[] = [];
  const activeRuns = [...ctl.engine.runs.values()].filter(r => r.status === "running" || r.status === "waiting");
  
  for (const run of activeRuns) {
    const p = ctl.engine.participants.get(run.participant_id);
    if (!p) continue;
    
    // Find claimed nodes or the run's root node
    const roots = new Set<string>();
    roots.add(run.node_id);
    for (const node of ctl.view.nodes.values()) {
      if (node.claim?.run_id === run.id) roots.add(node.id);
    }
    
    for (const rootId of roots) {
      const rootBox = ctl.display.layout.boxes.get(rootId);
      if (!rootBox) continue;
      
      let minX = rootBox.x, minY = rootBox.y, maxX = rootBox.x + rootBox.w, maxY = rootBox.y + rootBox.h;
      
      // Include all descendants
      const stack = [rootId];
      while (stack.length > 0) {
        const curr = stack.pop()!;
        const children = ctl.display.layout.children.get(curr) ?? [];
        for (const child of children) {
          const cb = ctl.display.layout.boxes.get(child);
          if (cb) {
            minX = Math.min(minX, cb.x);
            minY = Math.min(minY, cb.y);
            maxX = Math.max(maxX, cb.x + cb.w);
            maxY = Math.max(maxY, cb.y + cb.h);
            stack.push(child);
          }
        }
      }
      
      // Add padding
      const pad = 20;
      halos.push({
        id: `${run.id}-${rootId}`,
        color: p.color,
        box: { x: minX - pad, y: minY - pad, w: maxX - minX + pad * 2, h: maxY - minY + pad * 2 }
      });
    }
  }
  return halos;
}

function OffscreenMarkers(props: { ctl: MapController; region: { x: number; y: number; w: number; h: number } | null; cam: Camera; size: { w: number; h: number }; onFly: (id: string) => void }) {
  const { ctl, region, cam, size, onFly } = props;
  if (!region) return null;
  
  const activeRuns = [...ctl.engine.runs.values()].filter(r => r.status === "running" || r.status === "waiting");
  const markers: { id: string; x: number; y: number; angle: number; color: string; name: string }[] = [];
  
  const cx = region.x + region.w / 2;
  const cy = region.y + region.h / 2;
  
  for (const run of activeRuns) {
    const p = ctl.engine.participants.get(run.participant_id);
    if (!p) continue;
    
    const b = ctl.display.layout.boxes.get(run.node_id);
    if (!b) continue;
    
    // Check if outside region
    if (b.x + b.w > region.x && b.x < region.x + region.w && b.y + b.h > region.y && b.y < region.y + region.h) continue;
    
    const nx = b.x + b.w / 2;
    const ny = b.y + b.h / 2;
    
    const dx = nx - cx;
    const dy = ny - cy;
    const angle = Math.atan2(dy, dx);
    
    // Intersect with viewport rect (in screen space)
    // Viewport is 0,0 to size.w, size.h
    const scx = size.w / 2;
    const scy = size.h / 2;
    
    let tx = scx, ty = scy;
    const absCos = Math.abs(Math.cos(angle));
    const absSin = Math.abs(Math.sin(angle));
    
    if (scx * absSin > scy * absCos) {
      // Intersects top or bottom
      ty = Math.sign(Math.sin(angle)) * scy;
      tx = ty / Math.tan(angle);
    } else {
      // Intersects left or right
      tx = Math.sign(Math.cos(angle)) * scx;
      ty = tx * Math.tan(angle);
    }
    
    // Padding from edge
    const pad = 20;
    const len = Math.hypot(tx, ty);
    const scale = Math.max(0, len - pad) / len;
    
    markers.push({
      id: run.node_id,
      x: scx + tx * scale,
      y: scy + ty * scale,
      angle: angle * 180 / Math.PI,
      color: p.color,
      name: p.name
    });
  }
  
  return (
    <div className="mm-offscreen-markers">
      {markers.map((m, i) => (
        <div 
          key={i} 
          className="mm-offscreen-marker" 
          style={{ left: m.x, top: m.y, transform: `translate(-50%, -50%) rotate(${m.angle}deg)`, "--mm-agent": m.color } as any}
          onClick={() => onFly(m.id)}
          title={`Fly to ${m.name}`}
        >
          <svg width="24" height="24" viewBox="0 0 24 24"><path d="M12 2L22 20L12 17L2 20L12 2Z" fill="currentColor" /></svg>
        </div>
      ))}
    </div>
  );
}
