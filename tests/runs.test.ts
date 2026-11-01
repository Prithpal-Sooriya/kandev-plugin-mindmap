import { describe, expect, it } from "vitest";
import type { AgentProfile, MapNode } from "../ui/src/contract";
import { agentMenuItems } from "../ui/src/canvas/NodeMenu";
import { needsAnswer } from "../ui/src/canvas/QuestionAnswer";
import { canMessage, canStop } from "../ui/src/canvas/RunActions";
import {
  buildRequest, defaultSwarm, describeDraft, hasErrors, initialDraft, nextLens, pickProfile, unavailableReason, validateDraft,
  type DraftContext,
} from "../ui/src/canvas/runForm";
import { mapApi } from "../ui/src/host/http";
import { mapLink, RunTaskCache } from "../ui/src/kandev/TaskPanel";

const PROFILES: AgentProfile[] = [
  { id: "p-cursor", name: "Cursor Fast", harness: "cursor" },
  { id: "p-claude", name: "Claude Researcher", harness: "claude" },
  { id: "p-pi", name: "Pi Flash", harness: "pi" },
];
const node = { id: "nd_aaaaaaaaaaaa", kind: "idea", title: "Pricing" } as const;
const ctx = (over: Partial<DraftContext> = {}): DraftContext => ({ node, rootId: "nd_rootrootroot", profiles: PROFILES, ...over });

describe("run dialog: profiles", () => {
  it("prefers the remembered profile, then Claude, then the first one", () => {
    expect(pickProfile(PROFILES, "p-pi")).toBe("p-pi");
    expect(pickProfile(PROFILES, "gone")).toBe("p-claude");
    expect(pickProfile(PROFILES.filter((p) => p.harness !== "claude"))).toBe("p-cursor");
    expect(pickProfile([])).toBe("");
  });

  it("spreads a default swarm across harnesses", () => {
    const s = defaultSwarm(PROFILES);
    expect(s.map((x) => x.lens)).toEqual(["researcher", "skeptic", "examples_hunter"]);
    expect(new Set(s.map((x) => x.profileId)).size).toBe(3);
    expect(s[0]!.profileId).toBe("p-claude");
    expect(defaultSwarm([PROFILES[0]!]).every((x) => x.profileId === "p-cursor")).toBe(true);
  });

  it("adds the least used lens next", () => {
    expect(nextLens([{ lens: "researcher", profileId: "" }, { lens: "skeptic", profileId: "" }])).toBe("builder");
  });
});

describe("run dialog: drafts and requests", () => {
  it("builds a one-agent run", () => {
    const d = initialDraft("run", ctx());
    d.template = "critique";
    d.goal = "  Find weak spots.  ";
    expect(hasErrors(validateDraft(d, PROFILES))).toBe(false);
    expect(buildRequest(d)).toEqual({
      kind: "run",
      req: {
        node_id: node.id, template: "critique", profile_id: "p-claude", mode: "apply", goal: "Find weak spots.",
        budget: { max_nodes: 20, max_depth: 3, max_subagents: 1, max_minutes: 15 },
      },
    });
    expect(describeDraft(d, PROFILES)).toBe("Start Claude Researcher");
  });

  it("garden runs on the root and always proposes", () => {
    const d = initialDraft("garden", ctx());
    d.mode = "apply";
    const r = buildRequest(d);
    expect(r.kind).toBe("run");
    expect(r.req).toMatchObject({ node_id: "nd_rootrootroot", template: "garden", mode: "propose" });
  });

  it("explore and execute map to their templates; execute keeps the repo", () => {
    expect(buildRequest(initialDraft("explore", ctx())).req).toMatchObject({ template: "explore", budget: { max_nodes: 60, max_subagents: 4 } });
    const ex = initialDraft("execute", ctx({ node: { ...node, kind: "action" }, mapRepoId: "repo-1" }));
    expect(buildRequest(ex).req).toMatchObject({ template: "execute", repo_id: "repo-1" });
    expect(unavailableReason("execute", node, PROFILES)).toMatch(/action nodes/);
    expect(unavailableReason("execute", { kind: "action" }, PROFILES)).toBeNull();
    expect(unavailableReason("run", node, [])).toMatch(/No agent profiles/);
  });

  it("builds a swarm with one entry per lens", () => {
    const d = initialDraft("swarm", ctx());
    d.budget = { nodes: "8", depth: "", subagents: "", minutes: "" };
    const r = buildRequest(d);
    expect(r.kind).toBe("swarm");
    if (r.kind !== "swarm") return;
    expect(r.req.agents).toHaveLength(3);
    expect(r.req.budget).toEqual({ max_nodes: 8 });
    expect(r.req.node_id).toBe(node.id);
    expect(describeDraft(d, PROFILES)).toBe("Start 3 agents");
  });

  it("validates profile, custom goal, budgets and swarm size", () => {
    const d = initialDraft("run", ctx());
    d.profileId = "missing";
    d.template = "custom";
    d.budget = { ...d.budget, nodes: "ten" };
    expect(Object.keys(validateDraft(d, PROFILES)).sort()).toEqual(["budget", "goal", "profile"]);
    d.budget = { ...d.budget, nodes: "0" };
    expect(validateDraft(d, PROFILES).budget).toBeDefined();
    d.budget = { ...d.budget, nodes: "" };
    expect(validateDraft(d, PROFILES).budget).toBeUndefined();
    const s = initialDraft("swarm", ctx());
    s.swarm = [];
    expect(validateDraft(s, PROFILES).swarm).toMatch(/at least one/);
    s.swarm = Array.from({ length: 9 }, () => ({ lens: "skeptic" as const, profileId: "p-pi" }));
    expect(validateDraft(s, PROFILES).swarm).toMatch(/At most 8/);
    s.swarm = [{ lens: "skeptic", profileId: "" }];
    expect(validateDraft(s, PROFILES).swarm).toMatch(/profile/);
  });

  it("leaves blank budgets to the server", () => {
    const d = initialDraft("run", ctx());
    d.budget = { nodes: "", depth: "", subagents: "", minutes: "" };
    expect(buildRequest(d).req).not.toHaveProperty("budget");
  });
});

describe("run actions and menus", () => {
  it("offers stop on live runs and message on runs with a task or swarms", () => {
    expect(canStop({ status: "running" })).toBe(true);
    expect(canStop({ status: "review" })).toBe(true);
    expect(canStop({ status: "done" })).toBe(false);
    expect(canMessage({ status: "done", task_id: "t1", template: "research" })).toBe(true);
    expect(canMessage({ status: "running", template: "swarm" })).toBe(true);
    expect(canMessage({ status: "running", template: "research" })).toBe(false);
    expect(canMessage({ status: "failed", task_id: "t1", template: "research" })).toBe(false);
  });

  it("only enables execute on action nodes", () => {
    const opened: string[] = [];
    const items = agentMenuItems({ kind: "idea" }, (k) => opened.push(k));
    expect(items.map((i) => i.id)).toEqual(["run", "swarm", "explore", "execute", "garden"]);
    expect(items.find((i) => i.id === "execute")!.disabled).toBe(true);
    items[1]!.run();
    expect(opened).toEqual(["swarm"]);
    expect(agentMenuItems({ kind: "action" }, () => {}).find((i) => i.id === "execute")!.disabled).toBe(false);
  });

  it("shows the answer bar only on open questions", () => {
    const q = { kind: "question", question: { run_id: "run_aaaaaaaaaaaa" } } as unknown as MapNode;
    expect(needsAnswer(q)).toBe(true);
    expect(needsAnswer({ ...q, question: { run_id: "run_aaaaaaaaaaaa", answer: "SMB" } } as MapNode)).toBe(false);
    expect(needsAnswer({ ...q, kind: "idea" } as MapNode)).toBe(false);
    expect(needsAnswer(undefined)).toBe(false);
  });
});

describe("run API routes", () => {
  it("calls the protocol §4 routes", async () => {
    const calls: [string, string, unknown][] = [];
    const api = mapApi(async <T>(method: string, path: string, body?: unknown) => {
      calls.push([method, path, body]);
      if (path.endsWith("/swarms") || path.endsWith("/runs")) return { runs: [] } as T;
      if (path === "/runs/tasks") return { task_ids: ["t1"] } as T;
      return undefined as T;
    });
    await api.startSwarm("mp_1", { node_id: "nd_1", agents: [{ lens: "skeptic" }] });
    await api.stopRun("run_1");
    await api.messageRun("run_1", "Focus");
    await api.answerQuestion("mp_1", "nd_q", "Yes");
    await api.repos("mp_1");
    await api.taskRun("task 1");
    expect(await api.runTasks()).toEqual(["t1"]);
    expect(calls).toEqual([
      ["POST", "/maps/mp_1/swarms", { node_id: "nd_1", agents: [{ lens: "skeptic" }] }],
      ["POST", "/runs/run_1/stop", undefined],
      ["POST", "/runs/run_1/message", { text: "Focus" }],
      ["POST", "/maps/mp_1/nodes/nd_q/answer", { answer: "Yes" }],
      ["GET", "/repos?map_id=mp_1", undefined],
      ["GET", "/tasks/task%201/run", undefined],
      ["GET", "/runs/tasks", undefined],
    ]);
  });
});

describe("task panel", () => {
  it("links back to the node in the map", () => {
    expect(mapLink("mp_1", "nd_2")).toBe("/mindmap?map=mp_1&node=nd_2");
  });

  it("answers visibility from a refreshed cache", async () => {
    let calls = 0;
    const cache = new RunTaskCache(async () => {
      calls++;
      return ["t1"];
    });
    expect(cache.has("other")).toBe(true); // unknown before the first load
    await cache.refresh();
    expect(cache.has("t1")).toBe(true);
    expect(cache.has("other")).toBe(false);
    cache.add("t2");
    expect(cache.has("t2")).toBe(true);
    expect(calls).toBe(1);
  });
});
