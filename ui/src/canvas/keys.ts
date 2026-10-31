// Keyboard-first editing, XMind style. A pure mapping from a key event to a
// command, so the bindings are unit-tested without a DOM.
import type { Dir } from "../layout/geometry";

export type Command =
  | { type: "addChild" }
  | { type: "addSibling" }
  | { type: "edit"; initial?: string }
  | { type: "commitEdit" }
  | { type: "cancelEdit" }
  | { type: "nav"; dir: Dir }
  | { type: "toggleCollapse" }
  | { type: "retire" }
  | { type: "undo" }
  | { type: "redo" }
  | { type: "palette" }
  | { type: "fit" }
  | { type: "zoomIn" }
  | { type: "zoomOut" }
  | { type: "deselect" }
  | { type: "inspector" }
  | { type: "selectRoot" };

export interface KeyLike {
  key: string;
  metaKey?: boolean;
  ctrlKey?: boolean;
  shiftKey?: boolean;
  altKey?: boolean;
  isComposing?: boolean;
}

export interface KeyContext {
  /** An inline title editor has focus. */
  editing: boolean;
  /** A node is selected. */
  selected: boolean;
  /** macOS: ⌘ is the primary modifier; elsewhere Ctrl. */
  mac: boolean;
}

const ARROWS: Record<string, Dir> = { ArrowLeft: "left", ArrowRight: "right", ArrowUp: "up", ArrowDown: "down" };

export function keyToCommand(e: KeyLike, ctx: KeyContext): Command | null {
  if (e.isComposing) return null;
  const mod = ctx.mac ? !!e.metaKey : !!e.ctrlKey;
  const key = e.key.length === 1 ? e.key.toLowerCase() : e.key;

  if (mod && key === "k") return { type: "palette" };
  if (ctx.editing) {
    // The inline editor owns every other key: Enter commits, Esc cancels,
    // Tab commits and adds a child (so a chain of Tabs builds a branch).
    if (e.key === "Enter" && !e.shiftKey) return { type: "commitEdit" };
    if (e.key === "Escape") return { type: "cancelEdit" };
    if (e.key === "Tab") return { type: "commitEdit" };
    return null;
  }
  if (mod && key === "z") return e.shiftKey ? { type: "redo" } : { type: "undo" };
  if (mod && key === "y" && !ctx.mac) return { type: "redo" };
  if (mod && (key === "0" || key === "1")) return { type: "fit" };
  if (mod && (key === "=" || key === "+")) return { type: "zoomIn" };
  if (mod && key === "-") return { type: "zoomOut" };
  if (e.metaKey || e.ctrlKey || e.altKey) return null;

  if (!ctx.selected) {
    if (ARROWS[e.key] || e.key === "Enter" || e.key === "Tab") return { type: "selectRoot" };
    return null;
  }
  if (ARROWS[e.key]) return { type: "nav", dir: ARROWS[e.key]! };
  switch (e.key) {
    case "Tab":
      return e.shiftKey ? null : { type: "addChild" };
    case "Insert":
      return { type: "addChild" };
    case "Enter":
      return e.shiftKey ? { type: "inspector" } : { type: "addSibling" };
    case "F2":
      return { type: "edit" };
    case " ":
      return { type: "toggleCollapse" };
    case "Delete":
    case "Backspace":
      return { type: "retire" };
    case "Escape":
      return { type: "deselect" };
  }
  // Typing a printable character starts editing and replaces the title.
  if (e.key.length === 1) return { type: "edit", initial: e.key };
  return null;
}
