import { describe, expect, it } from "vitest";
import { newId, type Op } from "../ui/src/contract";
import { applyOp, childrenOf, cloneDoc, type Doc } from "../ui/src/core/doc";
import type { OpDraft } from "../ui/src/core/sync";
import { UndoStack, inverseOf } from "../ui/src/core/undo";
import { child, docFrom, ROOT } from "./fixtures";

const apply = (d: Doc, drafts: OpDraft[]) => {
  for (const x of drafts) {
    applyOp(d, { id: newId("op"), map_id: "map_test00000001", rev: ++d.rev, actor: { participant_id: "pt_host00000001" }, ...x } as Op);
  }
};
const live = (d: Doc) =>
  [...d.nodes.values()].filter((n) => !n.deleted).map(({ rev: _r, updated_at: _u, ...n }) => n);

describe("undo", () => {
  const A = "nd_aaaaaaaaaaaa", B = "nd_bbbbbbbbbbbb";
  const base = () => docFrom([child(ROOT, "A", "V", A), child(A, "B", "V", B)]);

  const cases: [string, OpDraft[]][] = [
    ["create", [child(ROOT, "C", "a", "nd_cccccccccccc")]],
    ["title + tags", [{ type: "node.update", payload: { id: A, set: { title: "A!" }, add_tags: ["t"] } }]],
    ["collapse", [{ type: "node.update", payload: { id: A, set: { collapsed: true } } }]],
    ["move", [{ type: "node.move", payload: { id: B, parent_id: ROOT, order: "a" } }]],
    ["delete", [{ type: "node.delete", payload: { id: A } }]],
    ["confidence (cleared on undo)", [{ type: "node.update", payload: { id: A, set: { confidence: 0.4 } } }]],
  ];
  for (const [name, drafts] of cases) {
    it(`inverts ${name}`, () => {
      const d = base();
      const before = live(cloneDoc(d));
      const inv = inverseOf(d, drafts);
      apply(d, drafts);
      apply(d, inv);
      expect(live(d)).toEqual(before);
    });
  }

  it("redoes what was undone, and a new edit clears redo", () => {
    const d = base();
    const s = new UndoStack();
    const edit: OpDraft[] = [{ type: "node.update", payload: { id: A, set: { title: "Renamed" } } }];
    s.record(d, edit);
    apply(d, edit);
    apply(d, s.undo(d)!);
    expect(d.nodes.get(A)!.title).toBe("A");
    apply(d, s.redo(d)!);
    expect(d.nodes.get(A)!.title).toBe("Renamed");
    apply(d, s.undo(d)!);
    const c: OpDraft[] = [child(ROOT, "C", "a", "nd_cccccccccccc")];
    s.record(d, c);
    apply(d, c);
    expect(s.canRedo).toBe(false);
    apply(d, s.undo(d)!);
    expect(childrenOf(d, ROOT).map((n) => n.title)).toEqual(["A"]);
    apply(d, s.redo(d)!); // redo of a create is a restore of the same id
    expect(childrenOf(d, ROOT).map((n) => n.title)).toEqual(["A", "C"]);
  });
});
