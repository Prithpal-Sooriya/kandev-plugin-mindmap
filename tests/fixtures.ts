import { newId, type Op } from "../ui/src/contract";
import { applyOp, emptyDoc, type Doc } from "../ui/src/core/doc";
import type { OpDraft } from "../ui/src/core/sync";
import { seedDrafts } from "../ui/src/dev/seed";

export const ROOT = "nd_root00000001";

export function docFrom(drafts: OpDraft[], title = "Central topic"): Doc {
  const d = emptyDoc();
  d.map = {
    id: "map_test00000001", workspace_id: "ws", title, root_id: ROOT, settings: {}, rev: 0, created_by: "pt_host00000001",
    created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
  };
  let rev = 0;
  const all: OpDraft[] = [
    { type: "node.create", payload: { node: { id: ROOT, order: "V", title, kind: "topic" } } },
    ...drafts,
  ];
  for (const x of all) {
    applyOp(d, { id: newId("op"), map_id: d.map.id, rev: ++rev, actor: { participant_id: "pt_host00000001" }, ts: "2026-01-01T00:00:00Z", ...x } as Op);
  }
  d.rev = rev;
  return d;
}

export function seededDoc(n: number, seed = 1): Doc {
  return docFrom(seedDrafts(ROOT, n, seed));
}

export function child(parent: string, title: string, order: string, id: string): OpDraft {
  return { type: "node.create", payload: { node: { id, parent_id: parent, order, title, kind: "idea" } } };
}
