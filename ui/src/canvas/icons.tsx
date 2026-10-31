import type { Harness, NodeKind } from "../contract";
import { h } from "../runtime";

// 24-unit stroke icons, drawn small. Kept as path data so the bundle stays lean.
const KIND_PATHS: Record<NodeKind, string> = {
  topic: "M12 3a9 9 0 1 0 0 18a9 9 0 1 0 0-18M12 8a4 4 0 1 0 0 8a4 4 0 1 0 0-8",
  idea: "M9 18h6M10 21h4M12 3a6 6 0 0 0-4 10.5c.7.7 1 1.5 1 2.5h6c0-1 .3-1.8 1-2.5A6 6 0 0 0 12 3",
  research: "M11 4a7 7 0 1 0 0 14a7 7 0 1 0 0-14M20 20l-4-4",
  example: "M6 3h9l4 4v14H6zM14 3v5h5M9 13l2 2l4-4",
  question: "M9 9a3 3 0 1 1 4 2.8c-.7.3-1 1-1 1.7V15M12 19h.01",
  decision: "M12 3l9 9l-9 9l-9-9zM12 8v8M8 12h8",
  risk: "M12 4L2 20h20zM12 10v4M12 17h.01",
  action: "M5 12l4 4L19 6",
  doc: "M6 3h9l4 4v14H6zM14 3v5h5M9 13h6M9 17h6",
};

export const KIND_LABEL: Record<NodeKind, string> = {
  topic: "Topic", idea: "Idea", research: "Research", example: "Example", question: "Question",
  decision: "Decision", risk: "Risk", action: "Action", doc: "Doc",
};

export function KindIcon(props: { kind: NodeKind; size?: number }) {
  const s = props.size ?? 12;
  return h(
    "svg",
    { className: `mm-kind-icon mm-kind-${props.kind}`, width: s, height: s, viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: 2.2, strokeLinecap: "round", strokeLinejoin: "round", "aria-hidden": "true" },
    h("path", { d: KIND_PATHS[props.kind] }),
  );
}

export const HARNESS_GLYPH: Record<Harness, string> = { claude: "✳", cursor: "↖", pi: "π", codex: "◇", other: "◎" };
export const HARNESS_LABEL: Record<Harness, string> = { claude: "Claude", cursor: "Cursor", pi: "pi", codex: "Codex", other: "Agent" };

export function Icon(props: { d: string; size?: number; className?: string }) {
  const s = props.size ?? 14;
  return h(
    "svg",
    { className: props.className, width: s, height: s, viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: 2, strokeLinecap: "round", strokeLinejoin: "round", "aria-hidden": "true" },
    h("path", { d: props.d }),
  );
}

export const ICONS = {
  back: "M15 18l-6-6l6-6",
  plus: "M12 5v14M5 12h14",
  search: "M11 4a7 7 0 1 0 0 14a7 7 0 1 0 0-14M20 20l-4-4",
  fit: "M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5",
  ghost: "M5 21V10a7 7 0 0 1 14 0v11l-3-2l-2 2l-2-2l-2 2l-2-2zM9 10h.01M15 10h.01",
  close: "M6 6l12 12M18 6L6 18",
  check: "M5 12l4 4L19 6",
  link: "M10 14a4 4 0 0 0 6 0l3-3a4 4 0 0 0-6-6l-1 1M14 10a4 4 0 0 0-6 0l-3 3a4 4 0 0 0 6 6l1-1",
  image: "M4 5h16v14H4zM4 15l4-4l5 5M14 14l2-2l4 4M15 9h.01",
  panel: "M4 4h16v16H4zM15 4v16",
  sun: "M12 8a4 4 0 1 0 0 8a4 4 0 1 0 0-8M12 2v2M12 20v2M2 12h2M20 12h2M5 5l1.5 1.5M17.5 17.5L19 19M5 19l1.5-1.5M17.5 6.5L19 5",
  moon: "M20 14A8 8 0 1 1 10 4a6 6 0 0 0 10 10",
  bolt: "M13 2L4 14h7l-1 8l9-12h-7z",
  edit: "M4 20h4L19 9l-4-4L4 16zM14 6l4 4",
  undo: "M9 14L4 9l5-5M4 9h11a5 5 0 0 1 0 10h-3",
  redo: "M15 14l5-5l-5-5M20 9H9a5 5 0 0 0 0 10h3",
};

export function MindmapIcon(props: { className?: string; size?: number }) {
  const size = props.size ?? 16;
  return h(
    "svg",
    { xmlns: "http://www.w3.org/2000/svg", width: size, height: size, viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: 2, strokeLinecap: "round", strokeLinejoin: "round", className: props.className, "aria-hidden": "true" },
    h("circle", { cx: 12, cy: 12, r: 3 }),
    h("circle", { cx: 4, cy: 5, r: 2 }),
    h("circle", { cx: 20, cy: 5, r: 2 }),
    h("circle", { cx: 4, cy: 19, r: 2 }),
    h("circle", { cx: 20, cy: 19, r: 2 }),
    h("path", { d: "M10 10L5.5 6.5M14 10l4.5-3.5M10 14l-4.5 3.5M14 14l4.5 3.5" }),
  );
}
