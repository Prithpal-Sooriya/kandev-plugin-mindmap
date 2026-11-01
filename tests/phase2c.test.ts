import ReactLib from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeAll, describe, expect, it } from "vitest";
import { ContractError, type AgentProfile, type DocTree, type MapNode, type Participant, type Run, type SuggestResponse } from "../ui/src/contract";
import { CompileForm, type CompileFormProps } from "../ui/src/canvas/CompileDialog";
import {
  MAX_COMPILE_SECTIONS, buildCompileRequest, compileRoot, compileUnavailable, describeCompile, initialCompileDraft, sectionCount, slugPreview,
  validateCompile,
} from "../ui/src/canvas/compileForm";
import type { MapController } from "../ui/src/canvas/controller";
import { AgentsDock, dockRuns } from "../ui/src/canvas/dock";
import { docFilePath, findDocTree } from "../ui/src/canvas/DocPanel";
import { renderMarkdown } from "../ui/src/canvas/markdown";
import { quickMenuItems } from "../ui/src/canvas/NodeMenu";
import { canApprove, needsYou, RunActions } from "../ui/src/canvas/RunActions";
import { suggestBranches, suggestMessage, SUGGEST_GIVE_UP_MS } from "../ui/src/canvas/suggest";
import { mapApi } from "../ui/src/host/http";
import { bindReact, h } from "../ui/src/runtime";

beforeAll(() => bindReact(ReactLib));

const profiles: AgentProfile[] = [
  { id: "prof-claude", name: "Claude", harness: "claude" },
  { id: "prof-codex", name: "Codex", harness: "codex" },
] as AgentProfile[];
const node = { id: "nd_pricing", kind: "idea", title: "Pricing" } as const;

describe("compile form", () => {
  it("starts on the branch, or the whole map from the root", () => {
    const d = initialCompileDraft({ node, rootId: "nd_root", profiles });
    expect(d).toMatchObject({ scope: "branch", profileId: "prof-claude", mode: "apply", target: "dir", repoId: "" });
    expect(compileRoot(d)).toBe("nd_pricing");
    expect(compileRoot({ ...d, scope: "map" })).toBe("nd_root");
    const r = initialCompileDraft({ node: { ...node, id: "nd_root" }, rootId: "nd_root", profiles, mapRepoId: "repo-1", defaultMode: "propose" });
    expect(r).toMatchObject({ scope: "map", target: "repo", repoId: "repo-1", mode: "propose" });
  });

  it("builds the request", () => {
    const d = initialCompileDraft({ node, rootId: "nd_root", profiles, mapRepoId: "repo-1" });
    expect(buildCompileRequest(d)).toEqual({ node_id: "nd_pricing", profile_id: "prof-claude", mode: "apply", target: "repo", repo_id: "repo-1" });
    expect(buildCompileRequest({ ...d, scope: "map", target: "dir", slug: " Launch plan ", mode: "propose" }))
      .toEqual({ node_id: "nd_root", profile_id: "prof-claude", mode: "propose", target: "dir", slug: "Launch plan" });
  });

  it("validates", () => {
    const d = initialCompileDraft({ node, rootId: "nd_root", profiles });
    expect(validateCompile(d, profiles, 3)).toEqual({});
    expect(validateCompile({ ...d, profileId: "gone" }, profiles, 3).profile).toBeTruthy();
    expect(validateCompile({ ...d, slug: "!!!" }, profiles, 3).slug).toBeTruthy();
    expect(validateCompile({ ...d, target: "repo", repoId: "" }, profiles, 3).repo).toBeTruthy();
    expect(validateCompile(d, profiles, MAX_COMPILE_SECTIONS + 1).sections).toContain(String(MAX_COMPILE_SECTIONS));
    expect(compileUnavailable({ kind: "doc" }, profiles, "branch")).toContain("compiled doc");
    expect(compileUnavailable({ kind: "doc" }, profiles, "map")).toBeNull();
    expect(compileUnavailable({ kind: "idea" }, [], "branch")).toContain("No agent profiles");
  });

  it("counts sections and names files like the server", () => {
    const kids: Record<string, { kind: MapNode["kind"] }[]> = { a: [{ kind: "idea" }, { kind: "doc" }, { kind: "risk" }], leaf: [] };
    expect(sectionCount("a", (id) => kids[id] ?? [])).toBe(2);
    expect(sectionCount("leaf", (id) => kids[id] ?? [])).toBe(1);
    expect(describeCompile(1)).toBe("Compile 1 section");
    expect(describeCompile(3)).toBe("Compile 3 sections");
    expect(slugPreview("", "Pricing & tiers!")).toBe("pricing-tiers");
    expect(slugPreview("Note App", "x")).toBe("note-app");
    expect(slugPreview("", "???")).toBe("mindmap");
  });

  const render = (patch: Partial<CompileFormProps> = {}) => {
    const draft = initialCompileDraft({ node, rootId: "nd_root", profiles });
    return renderToStaticMarkup(h(CompileForm, {
      draft, profiles, repos: [], nodeTitle: "Pricing", mapTitle: "Note app", sections: 3, touched: false, set: () => {}, ...patch,
    }));
  };

  it("renders scope, profile, mode and the file path", () => {
    const html = render();
    expect(html).toContain("This branch");
    expect(html).toContain("Whole map");
    expect(html).toContain("3 sections");
    expect(html).toContain("Claude (claude)");
    expect(html).toContain("Propose docs for review");
    expect(html).toContain("<code>pricing.md</code>");
    expect(html).not.toContain("Write files to");
  });

  it("offers the repository target when the map has one", () => {
    const draft = initialCompileDraft({ node, rootId: "nd_root", profiles, mapRepoId: "repo-1" });
    const html = render({ draft, mapRepoId: "repo-1", repos: [{ id: "repo-1", name: "acme/notes" } as never] });
    expect(html).toContain("Write files to");
    expect(html).toContain("acme/notes");
    expect(html).toContain("docs/mindmaps/pricing.md");
  });

  it("shows errors once touched and the section cap at once", () => {
    const draft = { ...initialCompileDraft({ node, rootId: "nd_root", profiles }), profileId: "" };
    expect(render({ draft })).not.toContain("Pick an agent profile.");
    expect(render({ draft, touched: true })).toContain("Pick an agent profile.");
    expect(render({ sections: 30 })).toContain("at most 24");
  });
});

// A MapController stand-in with what the dock and run actions read.
function fakeCtl(runs: Run[], me: Partial<Participant> | null, dockFilter: "all" | "needs" = "all") {
  return {
    engine: { runs: new Map(runs.map((r) => [r.id, r])), participants: new Map(), participant: me, activity: [] },
    view: { nodes: new Map([["nd_1", { id: "nd_1", title: "Pricing" }]]) },
    host: { kind: "standalone" },
    dockFilter,
    followingRun: null,
    bump() {},
  } as unknown as MapController;
}

const run = (id: string, patch: Partial<Run> = {}): Run => ({
  id, map_id: "mp_1", node_id: "nd_1", template: "compile", status: "running", participant_id: "pt_a", harness: "claude",
  mode: "apply", budget: {}, usage: { nodes: 0, subagents: 0 }, created_at: "2026-11-01T10:00:00Z", ...patch,
}) as Run;

describe("approve button", () => {
  const held = run("run_held", { status: "waiting", waiting_reason: "approval", parent_run_id: "run_coord" });
  const host = { is_host: true };
  const guest = { is_host: false };

  it("shows only for the host on runs held for approval", () => {
    expect(canApprove(held, host)).toBe(true);
    expect(canApprove(held, guest)).toBe(false);
    expect(canApprove(held, null)).toBe(false);
    expect(canApprove({ ...held, waiting_reason: "question" }, host)).toBe(false);
    expect(canApprove({ ...held, status: "running" }, host)).toBe(false);
  });

  it("renders on the dock row and in the inspector for the host only", () => {
    const coord = run("run_coord", { status: "waiting", waiting_reason: "approval", profile_name: "3 sections" });
    const runs = [coord, held, run("run_busy")];
    const dock = (me: Partial<Participant>) => renderToStaticMarkup(h(AgentsDock, { ctl: fakeCtl(runs, me), onReveal: () => {}, onClose: () => {} }));
    expect(dock(host).match(/data-testid="mm-dock-approve"/g)?.length).toBe(2);
    expect(dock(guest)).not.toContain("mm-dock-approve");
    const actions = (me: Partial<Participant>) => renderToStaticMarkup(h(RunActions, { ctl: fakeCtl(runs, me), run: held }));
    expect(actions(host)).toContain("mm-run-approve");
    expect(actions(guest)).not.toContain("mm-run-approve");
  });

  it("the needs-you filter keeps waiting runs and their parents", () => {
    const coord = run("run_coord", { status: "running" });
    const runs = [coord, held, run("run_busy", { parent_run_id: "run_coord" }), run("run_other")];
    expect(dockRuns(runs, "needs").map((r) => r.id)).toEqual(["run_coord", "run_held"]);
    expect(dockRuns(runs, "all")).toHaveLength(4);
    expect(needsYou({ status: "review" })).toBe(true);
    const html = renderToStaticMarkup(h(AgentsDock, { ctl: fakeCtl(runs, host, "needs"), onReveal: () => {}, onClose: () => {} }));
    expect(html).toContain("Needs you (1)");
    expect(html.match(/data-testid="mm-dock-run"/g)?.length).toBe(2);
  });
});

describe("suggest", () => {
  const done: SuggestResponse = { status: "done", job_id: "run_s1", proposal_id: "prp_1", node_ids: ["nd_a"], suggestions: [{ title: "Student discount", kind: "idea" }] };

  it("polls a slow job until it is done", async () => {
    const toasts: [string, string | undefined][] = [];
    const polls: string[] = [];
    let n = 0;
    const res = await suggestBranches({
      api: {
        suggest: async () => ({ status: "running", job_id: "run_s1", suggestions: [] }),
        suggestJob: async (_m, job) => {
          polls.push(job);
          return ++n < 2 ? { status: "running", job_id: job, suggestions: [] } : done;
        },
      },
      toast: (m, k) => toasts.push([m, k]),
      sleep: async () => {},
    }, "mp_1", "nd_1", "Pricing");
    expect(res?.status).toBe("done");
    expect(polls).toEqual(["run_s1", "run_s1"]);
    expect(toasts.at(-1)).toEqual(["1 suggested branch to review.", "success"]);
  });

  it("reports failures and gives up on a stuck job", async () => {
    const toasts: string[] = [];
    await suggestBranches({
      api: { suggest: async () => { throw new ContractError("budget_exceeded", "too many agents running"); }, suggestJob: async () => done },
      toast: (m) => toasts.push(m),
    }, "mp_1", "nd_1", "Pricing");
    expect(toasts.at(-1)).toContain("too many agents running");
    let t = 0;
    const stuck = await suggestBranches({
      api: { suggest: async () => ({ status: "running", job_id: "j", suggestions: [] }), suggestJob: async () => ({ status: "running", job_id: "j", suggestions: [] }) },
      toast: (m) => toasts.push(m),
      sleep: async () => {},
      now: () => (t += SUGGEST_GIVE_UP_MS / 3),
    }, "mp_1", "nd_1", "Pricing");
    expect(stuck?.status).toBe("running");
    expect(suggestMessage({ status: "failed", job_id: "j", suggestions: [], error: "not eligible" })).toBe("Suggest failed: not eligible");
    expect(suggestMessage({ status: "done", job_id: "j", suggestions: [] })).toBe("No new branches to suggest.");
  });

  it("menu entries: suggest is off on docs", () => {
    const calls: string[] = [];
    const items = quickMenuItems({ kind: "doc" }, { suggest: () => calls.push("s"), compile: () => calls.push("c") });
    expect(items.map((i) => [i.id, !!i.disabled])).toEqual([["suggest", true], ["compile", false]]);
    items[1]!.run();
    expect(calls).toEqual(["c"]);
  });
});

describe("docs and export", () => {
  const tree: DocTree = {
    map_id: "mp_1", slug: "note-app", root_node_id: "nd_root",
    single: { node_id: "nd_single", title: "Note app", path: "note-app.md" },
    index: { node_id: "nd_index", title: "Overview", path: "index.md", children: [{ node_id: "nd_sec", title: "Pricing", path: "pricing/index.md" }] },
  };

  it("finds the tree and the file of a doc", () => {
    expect(findDocTree([tree], "nd_sec")?.slug).toBe("note-app");
    expect(findDocTree([tree], "nd_single")?.slug).toBe("note-app");
    expect(findDocTree([tree], "nd_other")).toBeNull();
    expect(docFilePath(tree, { id: "nd_single", doc: { path: "note-app.md" } })).toBe("note-app.md");
    expect(docFilePath(tree, { id: "nd_sec", doc: { path: "pricing/index.md" } })).toBe("note-app/pricing/index.md");
  });

  it("renders relative doc links as text, never as hrefs", () => {
    const html = renderMarkdown("See [Pricing](pricing/index.md) and [x](javascript:alert(1)) and [site](https://example.com).");
    expect(html).toContain('<span class="mm-doclink" title="pricing/index.md">Pricing</span>');
    expect(html).not.toContain('href="javascript');
    expect(html).toContain('href="https://example.com"');
  });

  it("calls the compile, suggest, approve and export routes", async () => {
    const calls: [string, string, unknown][] = [];
    const blobs: string[] = [];
    const api = mapApi(async <T>(method: string, path: string, body?: unknown) => {
      calls.push([method, path, body]);
      return (path.endsWith("/compile") || path.endsWith("/approve") ? { runs: [] } : {}) as T;
    }, async (path) => {
      blobs.push(path);
      return new Blob(["zip"]);
    });
    await api.compile("mp_1", { node_id: "nd_1" });
    await api.suggest("mp_1", { node_id: "nd_1" });
    await api.suggestJob("mp_1", "run_s1");
    await api.approveRun("run_1");
    await api.docs("mp_1");
    await api.exportDocs("mp_1", "nd_d");
    await api.exportZip("mp_1");
    await api.exportZip("mp_1", "nd_d");
    expect(calls).toEqual([
      ["POST", "/maps/mp_1/compile", { node_id: "nd_1" }],
      ["POST", "/maps/mp_1/suggest", { node_id: "nd_1" }],
      ["GET", "/maps/mp_1/suggest/run_s1", undefined],
      ["POST", "/runs/run_1/approve", {}],
      ["GET", "/maps/mp_1/docs", undefined],
      ["POST", "/maps/mp_1/export?node_id=nd_d", {}],
    ]);
    expect(blobs).toEqual(["/maps/mp_1/export.zip", "/maps/mp_1/export.zip?node_id=nd_d"]);
  });
});
