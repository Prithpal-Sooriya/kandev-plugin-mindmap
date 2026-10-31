// One HTML node card. Pure function of its props: the canvas caches the
// element per node and reuses it when nothing changed, so React skips it.
import type { MapNode, Participant, Run } from "../contract";
import type { Box } from "../layout/tree";
import { Fragment, h } from "../runtime";
import { HarnessBadge, NewDot, StatusRing, TrailFlash, type RingStatus } from "./agent";
import type { Ghost } from "./display";
import { Icon, ICONS, KindIcon } from "./icons";

export interface CardProps {
  node: MapNode;
  box: Box;
  isRoot: boolean;
  selected: boolean;
  /** Remote participants selecting this node (outline colour). */
  remoteSelect: string | null;
  /** "<name> is editing…" soft-lock badge. */
  editingBy: { name: string; color: string } | null;
  ring: { status: RingStatus; run?: Run; color?: string; count?: number } | null;
  agents: Participant[];
  trail: { at: number; color: string } | null;
  unseen: boolean;
  ghosts: Ghost[] | undefined;
  dragging: boolean;
  hasThumb: boolean;
  thumbUrl?: string;
  /** Title currently being typed (hidden behind the editor). */
  hideTitle: boolean;
}

export function propsEqual(a: CardProps, b: CardProps): boolean {
  return (
    a.node === b.node && a.box === b.box && a.selected === b.selected && a.remoteSelect === b.remoteSelect &&
    a.editingBy?.name === b.editingBy?.name && a.ring?.status === b.ring?.status && a.ring?.color === b.ring?.color &&
    a.ring?.count === b.ring?.count && a.agents.length === b.agents.length && a.trail?.at === b.trail?.at &&
    a.unseen === b.unseen && a.ghosts === b.ghosts && a.dragging === b.dragging &&
    a.thumbUrl === b.thumbUrl && a.hideTitle === b.hideTitle && a.isRoot === b.isRoot
  );
}

export function NodeCard(p: CardProps) {
  const { node, box } = p;
  const ghostKinds = p.ghosts?.map((g) => g.kind) ?? [];
  const isGhost = node.id.startsWith("ghost:") || ghostKinds.includes("create");
  const cls = [
    "mm-node",
    `mm-k-${node.kind}`,
    p.isRoot ? "mm-root-node" : `mm-depth-${Math.min(box.depth, 3)}`,
    p.selected ? "mm-selected" : "",
    p.remoteSelect ? "mm-remote-sel" : "",
    isGhost ? "mm-ghost" : "",
    ghostKinds.includes("delete") ? "mm-ghost-delete" : "",
    ghostKinds.includes("update") ? "mm-ghost-update" : "",
    ghostKinds.includes("move") && !isGhost ? "mm-ghost-moving" : "",
    ghostKinds.includes("merge") ? "mm-ghost-merge" : "",
    p.dragging ? "mm-dragging" : "",
    node.kind === "research" && !node.sources?.length ? "mm-uncited" : "",
  ].filter(Boolean).join(" ");
  const style: Record<string, string | number> = {
    transform: `translate(${box.x}px,${box.y}px)`,
    width: box.w,
    height: box.h,
  };
  if (node.color) style["--mm-node-color"] = node.color;
  if (p.remoteSelect) style["--mm-remote"] = p.remoteSelect;
  const badges = !p.isRoot && (node.sources?.length || node.confidence !== undefined || node.assets?.length || node.tags?.length);
  const update = p.ghosts?.find((g) => g.kind === "update");

  return (
    <div className={cls} style={style} data-id={node.id} data-depth={box.depth} title={update?.detail ? `Proposed: ${update.detail}` : undefined}>
      {p.trail ? <TrailFlash at={p.trail.at} color={p.trail.color} /> : null}
      {p.ring ? <StatusRing status={p.ring.status} color={p.ring.color} count={p.ring.count} /> : null}
      <div className="mm-node-title" style={{ WebkitLineClamp: box.lines, visibility: p.hideTitle ? "hidden" : undefined }}>
        {!p.isRoot ? <KindIcon kind={node.kind} /> : null}
        <span className="mm-node-text">{node.title}</span>
      </div>
      {badges ? (
        <div className="mm-badges">
          {node.sources?.length ? <span className="mm-badge" title={`${node.sources.length} sources`}><Icon d={ICONS.link} size={10} />{node.sources.length}</span> : null}
          {node.confidence !== undefined ? <span className={`mm-badge mm-conf ${node.confidence < 0.4 ? "mm-conf-low" : node.confidence >= 0.75 ? "mm-conf-high" : ""}`} title="Confidence">{Math.round(node.confidence * 100)}%</span> : null}
          {node.assets?.length ? (
            p.thumbUrl ? <img className="mm-thumb" src={p.thumbUrl} alt="" /> : <span className="mm-badge" title={`${node.assets.length} attachments`}><Icon d={ICONS.image} size={10} />{node.assets.length}</span>
          ) : null}
          {node.tags?.slice(0, 2).map((t) => <span className="mm-tag" key={t}>#{t}</span>)}
        </div>
      ) : null}
      {box.hidden ? <span className="mm-collapsed-count" title={`${box.hidden} hidden`}>{box.hidden}</span> : null}
      {p.unseen ? <NewDot color={p.trail?.color} /> : null}
      {p.ring?.run && (p.ring.status === "running" || p.ring.status === "waiting") ? (
        <span className="mm-node-agents">
          {p.agents.length > 1 ? (
            <Fragment>{p.agents.slice(0, 3).map((a) => <HarnessBadge key={a.id} harness={a.harness ?? "other"} color={a.color} title={a.name} />)}</Fragment>
          ) : (
            <HarnessBadge harness={p.ring.run.harness} color={p.ring.color} title={p.agents[0]?.name} />
          )}
        </span>
      ) : null}
      {p.editingBy ? (
        <span className="mm-editing-badge" style={{ background: p.editingBy.color }}>{p.editingBy.name} is editing…</span>
      ) : null}
      {isGhost ? <span className="mm-ghost-tag">proposed</span> : null}
    </div>
  );
}
