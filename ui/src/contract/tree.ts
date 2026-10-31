// Pure tree checks for optimistic edits. Mirrors server/contract/tree.go so the
// UI can refuse an illegal drag before sending; the server re-checks.
import type { ErrorCode } from "./errors";

/** id -> parent id ("" for the root). Live (non-deleted) nodes only. */
export type ParentIndex = ReadonlyMap<string, string>;

export function buildParentIndex(nodes: Iterable<{ id: string; parent_id?: string; deleted?: boolean }>): Map<string, string> {
  const idx = new Map<string, string>();
  for (const n of nodes) if (!n.deleted) idx.set(n.id, n.parent_id ?? "");
  return idx;
}

export function isAncestor(idx: ParentIndex, anc: string, id: string): boolean {
  let cur = id;
  for (let i = 0; i <= idx.size; i++) {
    const p = idx.get(cur);
    if (p === undefined || p === "") return false;
    if (p === anc) return true;
    cur = p;
  }
  return false;
}

export function depth(idx: ParentIndex, id: string): number {
  if (!idx.has(id)) return -1;
  let d = 0;
  let cur = id;
  for (let i = 0; i <= idx.size; i++) {
    const p = idx.get(cur)!;
    if (p === "") return d;
    if (!idx.has(p)) return -1;
    d++;
    cur = p;
  }
  return -1;
}

/** Returns null if moving id under newParent is legal, else the error code. */
export function checkMove(idx: ParentIndex, rootId: string, id: string, newParent: string): ErrorCode | null {
  if (id === rootId) return "root_immutable";
  if (!idx.has(id) || !idx.has(newParent)) return "not_found";
  if (id === newParent || isAncestor(idx, id, newParent)) return "cycle";
  return null;
}

export function checkMerge(idx: ParentIndex, rootId: string, intoId: string, ids: readonly string[]): ErrorCode | null {
  if (!idx.has(intoId)) return "not_found";
  for (const id of ids) {
    if (id === rootId) return "root_immutable";
    if (!idx.has(id)) return "not_found";
    if (isAncestor(idx, id, intoId)) return "cycle";
  }
  return null;
}
