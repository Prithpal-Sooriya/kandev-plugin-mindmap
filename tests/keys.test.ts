import { describe, expect, it } from "vitest";
import { keyToCommand, type KeyContext } from "../ui/src/canvas/keys";

const sel: KeyContext = { editing: false, selected: true, mac: true };
const k = (key: string, mods: { meta?: boolean; ctrl?: boolean; shift?: boolean; alt?: boolean } = {}, ctx = sel) =>
  keyToCommand({ key, metaKey: mods.meta, ctrlKey: mods.ctrl, shiftKey: mods.shift, altKey: mods.alt }, ctx);

describe("keyboard command mapping", () => {
  it("maps the XMind-style editing keys", () => {
    expect(k("Tab")).toEqual({ type: "addChild" });
    expect(k("Enter")).toEqual({ type: "addSibling" });
    expect(k("F2")).toEqual({ type: "edit" });
    expect(k(" ")).toEqual({ type: "toggleCollapse" });
    expect(k("Delete")).toEqual({ type: "retire" });
    expect(k("Backspace")).toEqual({ type: "retire" });
    expect(k("Escape")).toEqual({ type: "deselect" });
    expect(k("Enter", { shift: true })).toEqual({ type: "inspector" });
  });

  it("starts editing with the typed character", () => {
    expect(k("q")).toEqual({ type: "edit", initial: "q" });
    expect(k("Q", { shift: true })).toEqual({ type: "edit", initial: "Q" });
    expect(keyToCommand({ key: "a", isComposing: true }, sel)).toBeNull();
  });

  it("navigates with arrows", () => {
    expect(k("ArrowLeft")).toEqual({ type: "nav", dir: "left" });
    expect(k("ArrowDown")).toEqual({ type: "nav", dir: "down" });
  });

  it("uses Cmd on macOS and Ctrl elsewhere for undo/redo and the palette", () => {
    expect(k("z", { meta: true })).toEqual({ type: "undo" });
    expect(k("z", { meta: true, shift: true })).toEqual({ type: "redo" });
    expect(k("Z", { meta: true, shift: true })).toEqual({ type: "redo" });
    expect(k("k", { meta: true })).toEqual({ type: "palette" });
    expect(k("z", { ctrl: true })).toBeNull(); // Ctrl+Z on mac is not undo
    const win = { ...sel, mac: false };
    expect(k("z", { ctrl: true }, win)).toEqual({ type: "undo" });
    expect(k("y", { ctrl: true }, win)).toEqual({ type: "redo" });
    expect(k("0", { meta: true })).toEqual({ type: "fit" });
  });

  it("lets the inline editor own keys while editing", () => {
    const ed = { ...sel, editing: true };
    expect(k("Enter", {}, ed)).toEqual({ type: "commitEdit" });
    expect(k("Escape", {}, ed)).toEqual({ type: "cancelEdit" });
    expect(k("Tab", {}, ed)).toEqual({ type: "commitEdit" });
    expect(k("a", {}, ed)).toBeNull();
    expect(k("Backspace", {}, ed)).toBeNull();
    expect(k("z", { meta: true }, ed)).toBeNull(); // native text undo
    expect(k("k", { meta: true }, ed)).toEqual({ type: "palette" });
  });

  it("selects the root when nothing is selected", () => {
    const none = { ...sel, selected: false };
    expect(k("ArrowRight", {}, none)).toEqual({ type: "selectRoot" });
    expect(k("a", {}, none)).toBeNull();
    expect(k("z", { meta: true }, none)).toEqual({ type: "undo" });
  });
});
