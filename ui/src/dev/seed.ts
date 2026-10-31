// Deterministic synthetic maps for perf work (?seed=N), tests and benchmarks.
import { orderBetween, type NodeKind } from "../contract";
import type { OpDraft } from "../core/sync";

const WORDS = [
  "soil", "compost", "irrigation", "seedlings", "pollinators", "shade", "raised beds", "mulch", "rotation", "harvest",
  "pests", "frost", "trellis", "greenhouse", "herbs", "budget", "tools", "layout", "paths", "lighting", "rainwater",
  "native plants", "pruning", "fertiliser", "community", "schedule", "permits", "volunteers", "signage", "storage",
];
const KINDS: NodeKind[] = ["idea", "idea", "idea", "research", "example", "question", "decision", "risk", "action"];

/** Mulberry32: tiny seeded PRNG. */
export function rng(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/** Deterministic ids so seeded maps are reproducible. */
function seedId(i: number, salt: string): string {
  return `nd_seed${salt}${String(i).padStart(12, "0")}`;
}

/**
 * N node.create drafts under rootId (`salt`: alphanumerics that keep ids unique across seeded maps): ~8 main branches, then each new node
 * picks a parent biased towards shallow, not-too-crowded nodes (max depth 5).
 */
export function seedDrafts(rootId: string, n: number, seed = 1, salt = ""): OpDraft[] {
  const rand = rng(seed);
  const nodes: { id: string; depth: number; last: string; kids: number }[] = [{ id: rootId, depth: 0, last: "", kids: 0 }];
  const out: OpDraft[] = [];
  for (let i = 0; i < n; i++) {
    let parent = nodes[0]!;
    if (i >= 8) {
      for (let tries = 0; tries < 8; tries++) {
        const cand = nodes[1 + Math.floor(rand() * (nodes.length - 1))]!;
        if (cand.depth < 5 && cand.kids < 9) {
          parent = cand;
          if (rand() < 0.6) break;
        }
      }
      if (parent === nodes[0]) parent = nodes[1 + (i % Math.min(8, nodes.length - 1))]!;
    }
    const id = seedId(i, salt);
    const order = orderBetween(parent.last, "");
    parent.last = order;
    parent.kids++;
    const words = 1 + Math.floor(rand() * 4);
    const title = Array.from({ length: words }, () => WORDS[Math.floor(rand() * WORDS.length)]).join(" ");
    const kind = KINDS[Math.floor(rand() * KINDS.length)]!;
    out.push({
      type: "node.create",
      payload: {
        node: {
          id, parent_id: parent.id, order, kind, title: title[0]!.toUpperCase() + title.slice(1),
          ...(rand() < 0.15 ? { confidence: Math.round(rand() * 10) / 10 } : {}),
          ...(kind === "research" || kind === "example"
            ? { sources: [{ url: `https://example.com/${i}`, title: `Example source ${i}` }] }
            : {}),
        },
      },
    });
    nodes.push({ id, depth: parent.depth + 1, last: "", kids: 0 });
  }
  return out;
}
