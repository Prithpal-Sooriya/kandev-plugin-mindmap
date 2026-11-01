// State and rules of the "Run agent…" dialog, kept free of React so the
// vitest suite can check them. The dialog edits a RunDraft and turns it into
// a StartRunRequest or a SwarmRequest (protocol §4).
import {
  LENSES, type AgentProfile, type Budget, type Lens, type MapNode, type RunMode, type StartRunRequest, type SwarmRequest,
  type Template,
} from "../contract";

/** Which flow opened the dialog: one agent, a swarm, the explorer, the gardener or execute. */
export type RunKind = "run" | "swarm" | "explore" | "garden" | "execute";

export const RUN_KINDS: readonly RunKind[] = ["run", "swarm", "explore", "garden", "execute"];

export const KIND_TITLE: Record<RunKind, string> = {
  run: "Run agent",
  swarm: "Swarm this node",
  explore: "Explore from here",
  garden: "Garden the map",
  execute: "Execute this action",
};

/** Templates the one-agent flow offers. */
export const RUN_TEMPLATES = ["research", "expand", "critique", "examples", "custom"] as const satisfies readonly Template[];
export type RunTemplate = (typeof RUN_TEMPLATES)[number];

export const TEMPLATE_LABEL: Record<RunTemplate, string> = {
  research: "Research",
  expand: "Expand branches",
  critique: "Critique",
  examples: "Find examples",
  custom: "Custom goal",
};

export const LENS_LABEL: Record<Lens, string> = {
  researcher: "Researcher",
  skeptic: "Skeptic",
  builder: "Builder",
  contrarian: "Contrarian",
  examples_hunter: "Examples hunter",
  futurist: "Futurist",
};

export const MAX_SWARM = 8;

export interface SwarmSlot {
  lens: Lens;
  profileId: string;
}

export interface BudgetDraft {
  nodes: string;
  depth: string;
  subagents: string;
  minutes: string;
}

export interface RunDraft {
  kind: RunKind;
  nodeId: string;
  template: RunTemplate;
  profileId: string;
  mode: RunMode;
  goal: string;
  repoId: string;
  budget: BudgetDraft;
  swarm: SwarmSlot[];
}

export interface DraftContext {
  node: Pick<MapNode, "id" | "kind" | "title">;
  rootId: string;
  profiles: AgentProfile[];
  /** Map setting or last used profile. */
  preferredProfileId?: string;
  mapRepoId?: string;
}

/** Default budgets per flow; the server fills anything left blank (protocol §9). */
export const DEFAULT_BUDGETS: Record<RunKind, Budget> = {
  run: { max_nodes: 20, max_depth: 3, max_subagents: 1, max_minutes: 15 },
  swarm: { max_nodes: 20, max_depth: 3, max_subagents: 1, max_minutes: 20 },
  explore: { max_nodes: 60, max_depth: 4, max_subagents: 4, max_minutes: 30 },
  garden: { max_nodes: 30, max_depth: 6, max_subagents: 1, max_minutes: 15 },
  execute: { max_nodes: 10, max_depth: 2, max_subagents: 1, max_minutes: 60 },
};

function budgetDraft(b: Budget): BudgetDraft {
  const s = (n?: number) => (n ? String(n) : "");
  return { nodes: s(b.max_nodes), depth: s(b.max_depth), subagents: s(b.max_subagents), minutes: s(b.max_minutes) };
}

/** The preferred profile if it still exists, else the first Claude, else the first one. */
export function pickProfile(profiles: AgentProfile[], preferred?: string): string {
  if (preferred && profiles.some((p) => p.id === preferred)) return preferred;
  return (profiles.find((p) => p.harness === "claude") ?? profiles[0])?.id ?? "";
}

/**
 * Default swarm: three lenses, spread across harnesses so the swarm is mixed
 * when more than one is installed.
 */
export function defaultSwarm(profiles: AgentProfile[], preferred?: string, size = 3): SwarmSlot[] {
  const first = pickProfile(profiles, preferred);
  const byHarness = new Map<string, string>();
  if (first) byHarness.set(profiles.find((p) => p.id === first)!.harness, first);
  for (const p of profiles) if (!byHarness.has(p.harness)) byHarness.set(p.harness, p.id);
  const ids = [...byHarness.values()];
  const lenses: Lens[] = ["researcher", "skeptic", "examples_hunter", "builder", "contrarian", "futurist"];
  return lenses.slice(0, Math.min(size, MAX_SWARM)).map((lens, i) => ({ lens, profileId: ids.length ? ids[i % ids.length]! : "" }));
}

/** The next lens not yet in the swarm (or the least used one). */
export function nextLens(slots: SwarmSlot[]): Lens {
  const used = new Map<Lens, number>(LENSES.map((l) => [l, 0]));
  for (const s of slots) used.set(s.lens, (used.get(s.lens) ?? 0) + 1);
  return [...LENSES].sort((a, b) => used.get(a)! - used.get(b)!)[0]!;
}

export function initialDraft(kind: RunKind, ctx: DraftContext): RunDraft {
  const profileId = pickProfile(ctx.profiles, ctx.preferredProfileId);
  return {
    kind,
    nodeId: kind === "garden" ? ctx.rootId : ctx.node.id,
    template: "research",
    profileId,
    mode: kind === "garden" ? "propose" : "apply",
    goal: "",
    repoId: kind === "execute" ? ctx.mapRepoId ?? "" : "",
    budget: budgetDraft(DEFAULT_BUDGETS[kind]),
    swarm: kind === "swarm" ? defaultSwarm(ctx.profiles, ctx.preferredProfileId) : [],
  };
}

/** Why the flow cannot start on this node, if it cannot. */
export function unavailableReason(kind: RunKind, node: Pick<MapNode, "kind">, profiles: AgentProfile[]): string | null {
  if (!profiles.length) return "No agent profiles found. Add one in Kandev settings first.";
  if (kind === "execute" && node.kind !== "action") return "Execute works on action nodes. Change the node kind to Action first.";
  return null;
}

function num(s: string): number | undefined | null {
  const t = s.trim();
  if (!t) return undefined;
  if (!/^\d+$/.test(t)) return null;
  const n = Number(t);
  return n > 0 && n <= 10_000 ? n : null;
}

export type DraftErrors = Partial<Record<"profile" | "goal" | "budget" | "swarm" | "repo", string>>;

export function validateDraft(d: RunDraft, profiles: AgentProfile[]): DraftErrors {
  const errs: DraftErrors = {};
  const known = new Set(profiles.map((p) => p.id));
  if (d.kind === "swarm") {
    if (!d.swarm.length) errs.swarm = "Add at least one agent.";
    else if (d.swarm.length > MAX_SWARM) errs.swarm = `At most ${MAX_SWARM} agents.`;
    else if (d.swarm.some((s) => !known.has(s.profileId))) errs.swarm = "Pick a profile for every agent.";
  } else if (!known.has(d.profileId)) errs.profile = "Pick an agent profile.";
  if (d.kind === "run" && d.template === "custom" && !d.goal.trim()) errs.goal = "Describe what the agent should do.";
  if (d.goal.length > 4000) errs.goal = "Keep the goal under 4000 characters.";
  for (const v of Object.values(d.budget)) {
    if (num(v) === null) {
      errs.budget = "Budgets are whole numbers above zero (or blank for the default).";
      break;
    }
  }
  return errs;
}

export function hasErrors(e: DraftErrors): boolean {
  return Object.keys(e).length > 0;
}

function toBudget(b: BudgetDraft): Budget | undefined {
  const out: Budget = {};
  const nodes = num(b.nodes), depth = num(b.depth), subs = num(b.subagents), mins = num(b.minutes);
  if (nodes) out.max_nodes = nodes;
  if (depth) out.max_depth = depth;
  if (subs) out.max_subagents = subs;
  if (mins) out.max_minutes = mins;
  return Object.keys(out).length ? out : undefined;
}

export function templateFor(d: RunDraft): Template {
  switch (d.kind) {
    case "run":
      return d.template;
    case "swarm":
      return "swarm";
    default:
      return d.kind;
  }
}

export type DraftRequest = { kind: "run"; req: StartRunRequest } | { kind: "swarm"; req: SwarmRequest };

export function buildRequest(d: RunDraft): DraftRequest {
  const goal = d.goal.trim() || undefined;
  const budget = toBudget(d.budget);
  if (d.kind === "swarm") {
    return {
      kind: "swarm",
      req: {
        node_id: d.nodeId, mode: d.mode, ...(goal ? { goal } : {}), ...(budget ? { budget } : {}),
        agents: d.swarm.map((s) => ({ lens: s.lens, profile_id: s.profileId })),
      },
    };
  }
  const req: StartRunRequest = { node_id: d.nodeId, template: templateFor(d), profile_id: d.profileId, mode: d.kind === "garden" ? "propose" : d.mode };
  if (goal) req.goal = goal;
  if (budget) req.budget = budget;
  if (d.repoId) req.repo_id = d.repoId;
  return { kind: "run", req };
}

/** A one-line summary for the confirm button and toasts. */
export function describeDraft(d: RunDraft, profiles: AgentProfile[]): string {
  const name = (id: string) => profiles.find((p) => p.id === id)?.name ?? "agent";
  if (d.kind === "swarm") {
    const n = d.swarm.length;
    return `Start ${n} agent${n === 1 ? "" : "s"}`;
  }
  return `Start ${name(d.profileId)}`;
}
