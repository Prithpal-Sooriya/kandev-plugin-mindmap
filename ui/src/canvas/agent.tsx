// Agent visibility building blocks (status rings, harness badge, write-trail
// flash, "new" dot, avatars). Phase 2b's dock and inspector reuse them.
import type { Harness, MapNode, Participant, Run } from "../contract";
import { h } from "../runtime";
import { HARNESS_GLYPH, HARNESS_LABEL } from "./icons";

export type RingStatus = "queued" | "running" | "waiting" | "review" | "failed" | "done";

/** The ring a node shows: the most urgent of its runs, else its own status field. */
export function ringFor(node: MapNode, runs: Run[] | undefined): { status: RingStatus; run?: Run } | null {
  const rank: RingStatus[] = ["waiting", "failed", "running", "review", "queued", "done"];
  let best: { status: RingStatus; run?: Run } | null = null;
  for (const r of runs ?? []) {
    if (r.status === "stopped") continue;
    const s = r.status as RingStatus;
    if (!best || rank.indexOf(s) < rank.indexOf(best.status)) best = { status: s, run: r };
  }
  if (best) return best;
  switch (node.status) {
    case "claimed":
      return { status: "queued" };
    case "running":
    case "review":
    case "done":
    case "failed":
      return { status: node.status };
  }
  return null;
}

/** Ring drawn around a card: dashed (queued), pulsing (running), amber "?" (waiting), red (failed), fading check (done). */
export function StatusRing(props: { status: RingStatus; color?: string; count?: number }) {
  const { status, color, count } = props;
  return h(
    "div",
    {
      className: `mm-ring mm-ring-${status}`,
      style: color ? { "--mm-agent": color } : undefined,
      "aria-label": `agent ${status}`,
    },
    status === "waiting" ? h("span", { className: "mm-ring-badge mm-rb-q", title: "Waiting on you" }, "?") : null,
    status === "review" ? h("span", { className: "mm-ring-badge mm-rb-review", title: "Proposed changes to review" }, count ? String(count) : "R") : null,
    status === "failed" ? h("span", { className: "mm-ring-badge mm-rb-fail", title: "Run failed" }, "!") : null,
    status === "done" ? h("span", { className: "mm-ring-badge mm-rb-done", title: "Done" }, "✓") : null,
  );
}

export function HarnessBadge(props: { harness: Harness; color?: string; title?: string }) {
  return h(
    "span",
    {
      className: "mm-harness",
      style: props.color ? { background: props.color } : undefined,
      title: props.title ?? HARNESS_LABEL[props.harness],
    },
    HARNESS_GLYPH[props.harness],
  );
}

/** Stacked avatars for several agents on one node (or the header participant list). */
export function AvatarStack(props: { people: Participant[]; max?: number; size?: "sm" | "md" }) {
  const max = props.max ?? 5;
  const shown = props.people.slice(0, max);
  const extra = props.people.length - shown.length;
  return h(
    "div",
    { className: `mm-avatars mm-avatars-${props.size ?? "md"}` },
    ...shown.map((p) =>
      h(
        "span",
        {
          key: p.id,
          className: `mm-avatar${p.kind === "agent" ? " mm-avatar-agent" : ""}${p.online === false ? " mm-avatar-off" : ""}`,
          style: { background: p.color },
          title: p.kind === "agent" && !p.name.includes("(") ? `${p.name} (${HARNESS_LABEL[p.harness ?? "other"]})` : p.name,
        },
        p.kind === "agent" ? HARNESS_GLYPH[p.harness ?? "other"] : initials(p.name),
      ),
    ),
    extra > 0 ? h("span", { className: "mm-avatar mm-avatar-more" }, `+${extra}`) : null,
  );
}

/** Flash in the writer's colour; keyed by time so a second write replays the animation. */
export function TrailFlash(props: { at: number; color: string }) {
  return h("div", { key: props.at, className: "mm-trail", style: { "--mm-agent": props.color } });
}

export function NewDot(props: { color?: string }) {
  return h("span", { className: "mm-new-dot", style: props.color ? { background: props.color } : undefined, title: "New since you last looked" });
}

export function initials(name: string): string {
  const parts = name.trim().split(/\s+/);
  return ((parts[0]?.[0] ?? "?") + (parts.length > 1 ? parts.at(-1)![0]! : "")).toUpperCase();
}
